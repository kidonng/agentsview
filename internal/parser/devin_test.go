package parser

import (
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

func TestDevinDBPath(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)

	root := t.TempDir()
	cliDir := filepath.Join(root, "cli")
	require.NoError(os.MkdirAll(cliDir, 0o755))
	dbPath := filepath.Join(cliDir, devinDBFilename)
	require.NoError(os.WriteFile(dbPath, []byte("synthetic"), 0o644))

	assert.Equal(dbPath, devinDBPath(root))
	assert.Empty(devinDBPath(filepath.Join(root, "missing")))
	assert.Empty(devinDBPath(""))

	require.NoError(os.Remove(dbPath))
	require.NoError(os.Mkdir(dbPath, 0o755))
	assert.Empty(devinDBPath(root))
}

func TestListDevinSessionMeta(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)

	fixture := newDevinTestFixture(t,
		devinSessionRow{ID: "session-hidden", Title: "Hidden", WorkingDirectory: "/cwd/hidden", Model: "model-hidden", CreatedAt: new(int64(1_700_000_010)), LastActivityAt: new(int64(1_700_000_090)), Hidden: true},
		devinSessionRow{ID: "session-fallback", Title: "Fallback title", WorkingDirectory: "/cwd/fallback", Model: "model-fallback", CreatedAt: new(int64(1_700_000_020))},
		devinSessionRow{ID: "session-active", Title: "Active title", WorkingDirectory: "/cwd/active", Model: "model-active", CreatedAt: new(int64(1_700_000_030)), LastActivityAt: new(int64(1_700_000_080))},
		devinSessionRow{ID: "session-newest", Title: "Newest title", WorkingDirectory: "/cwd/newest", Model: "model-newest", CreatedAt: new(int64(1_700_000_040)), LastActivityAt: new(int64(1_700_000_095))},
	)

	metas, err := ListDevinSessionMeta(fixture.DBPath)
	require.NoError(err)
	require.Len(metas, 3)

	assert.Equal([]string{"session-newest", "session-active", "session-fallback"}, devinMetaIDs(metas))

	assert.Equal(fixture.sessionVirtualPath("session-newest"), metas[0].VirtualPath)
	assert.Equal("Newest title", metas[0].Title)
	assert.Equal("/cwd/newest", metas[0].CWD)
	assert.Equal("model-newest", metas[0].Model)
	assert.Equal(time.Unix(1_700_000_095, 0).UTC(), metas[0].UpdatedAt)
	assert.Equal(int64(1_700_000_095_000_000_000), metas[0].FileMtime)

	assert.Equal(time.Unix(1_700_000_020, 0).UTC(), metas[2].UpdatedAt)
	assert.Equal(int64(1_700_000_020_000_000_000), metas[2].FileMtime)

	for _, meta := range metas {
		assert.NotEqual("session-hidden", meta.RawSessionID)
	}
}

func TestListDevinSessionMetaAllowsMissingTimestamps(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)

	fixture := newDevinTestFixture(t,
		devinSessionRow{ID: "session-missing-times", Title: "Partial row", WorkingDirectory: "/cwd/partial", Model: "model-partial"},
	)

	metas, err := ListDevinSessionMeta(fixture.DBPath)
	require.NoError(err)
	require.Len(metas, 1)

	assert.Equal("session-missing-times", metas[0].RawSessionID)
	assert.True(metas[0].CreatedAt.IsZero())
	assert.True(metas[0].UpdatedAt.IsZero())
	assert.Zero(metas[0].FileMtime)
}

// Devin stores epoch seconds. A row carrying some other unit (milliseconds, in
// practice) must not be converted anyway: FileMtime is seconds*1e9, which
// overflows int64 above year 2262 and wraps to a far-future nanosecond value.
// devinApplyFileInfoTimes only ever raises Mtime, so a wrapped value can never
// be superseded by a real file mtime and the session stops resyncing.
func TestListDevinSessionMetaRejectsImplausibleTimestamps(t *testing.T) {
	tests := []struct {
		name           string
		lastActivityAt int64
		wantUpdatedAt  time.Time
		wantFileMtime  int64
	}{
		{
			name:           "epoch seconds are accepted",
			lastActivityAt: 1_700_000_095,
			wantUpdatedAt:  time.Unix(1_700_000_095, 0).UTC(),
			wantFileMtime:  1_700_000_095_000_000_000,
		},
		{
			name:           "largest nanosecond-representable second is accepted",
			lastActivityAt: 9_223_372_036,
			wantUpdatedAt:  time.Unix(9_223_372_036, 0).UTC(),
			wantFileMtime:  9_223_372_036_000_000_000,
		},
		{
			name:           "one second past the nanosecond range is rejected",
			lastActivityAt: 9_223_372_037,
		},
		{
			name:           "millisecond value is rejected instead of overflowing",
			lastActivityAt: 1_700_000_095_000,
		},
		{
			name:           "negative value is rejected",
			lastActivityAt: -1,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			assert := assert.New(t)
			require := require.New(t)

			fixture := newDevinTestFixture(t,
				devinSessionRow{
					ID:               "session-units",
					Title:            "Units",
					WorkingDirectory: "/cwd/units",
					Model:            "model-units",
					CreatedAt:        new(tc.lastActivityAt),
					LastActivityAt:   new(tc.lastActivityAt),
				},
			)

			metas, err := ListDevinSessionMeta(fixture.DBPath)
			require.NoError(err)
			require.Len(metas, 1)

			assert.Equal(tc.wantUpdatedAt, metas[0].UpdatedAt)
			assert.Equal(tc.wantUpdatedAt, metas[0].CreatedAt)
			assert.Equal(tc.wantUpdatedAt, metas[0].LastActivity)
			assert.Equal(tc.wantFileMtime, metas[0].FileMtime)
		})
	}
}

func TestListDevinSessionMetaMissingDB(t *testing.T) {
	metas, err := ListDevinSessionMeta(filepath.Join(t.TempDir(), "cli", devinDBFilename))
	require.NoError(t, err)
	assert.Nil(t, metas)
}

func TestListDevinSessionMetaMalformedSchema(t *testing.T) {
	assert := assert.New(t)

	dbPath := filepath.Join(t.TempDir(), devinDBFilename)
	initDevinTestDB(t, dbPath)
	execDevinTestSQL(t, dbPath, `
		CREATE TABLE sessions (
			id TEXT PRIMARY KEY,
			payload TEXT
		);
	`)
	execDevinTestSQL(t, dbPath, `INSERT INTO sessions (id, payload) VALUES ('secret-session', 'top-secret-row-content')`)

	metas, err := ListDevinSessionMeta(dbPath)
	assert.Nil(metas)
	require.Error(t, err)
	assert.ErrorContains(err, "listing devin sessions")
	assert.NotContains(err.Error(), "top-secret-row-content")
	assert.NotContains(err.Error(), "secret-session")
}

func TestOpenDevinDBUsesReadOnlyMode(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)

	fixture := newDevinTestFixture(t,
		devinSessionRow{ID: "session-readonly", Title: "Read only", WorkingDirectory: "/tmp/readonly", Model: "db-model", CreatedAt: new(int64(1704103199)), LastActivityAt: new(int64(1704103265))},
	)

	db, err := openDevinDB(fixture.DBPath)
	require.NoError(err)
	defer db.Close()

	var journalMode string
	require.NoError(db.QueryRowContext(t.Context(), `PRAGMA journal_mode`).Scan(&journalMode))

	_, err = db.ExecContext(t.Context(), `INSERT INTO sessions (id) VALUES ('write-should-fail')`)
	require.Error(err)
	assert.ErrorContains(err, "readonly")
	assert.ErrorContains(err, "attempt to write")

	var count int
	require.NoError(db.QueryRowContext(t.Context(), `SELECT COUNT(*) FROM sessions`).Scan(&count))
	assert.Equal(1, count)

	metas, err := ListDevinSessionMeta(fixture.DBPath)
	require.NoError(err)
	assert.Equal([]string{"session-readonly"}, devinMetaIDs(metas))
	assert.NotEmpty(journalMode)
}

func TestOpenDevinDBWithSpecialCharPath(t *testing.T) {
	require := require.New(t)

	dir := filepath.Join(t.TempDir(), "pro#ject %41")
	require.NoError(os.MkdirAll(dir, 0o755))
	dbPath := filepath.Join(dir, devinDBFilename)

	writer, err := sql.Open("sqlite3", dbPath)
	require.NoError(err)
	_, err = writer.ExecContext(t.Context(), "CREATE TABLE sessions (id TEXT); INSERT INTO sessions VALUES ('session-1')")
	require.NoError(err)
	require.NoError(writer.Close())

	db, err := openDevinDB(dbPath)
	require.NoError(err)
	defer db.Close()

	var count int
	require.NoError(db.QueryRowContext(t.Context(), "SELECT count(*) FROM sessions").Scan(&count))
	assert.Equal(t, 1, count)

	_, err = db.ExecContext(t.Context(), "INSERT INTO sessions VALUES ('session-2')")
	require.Error(err, "mode=ro must survive special characters in the path")
}

func TestParseDevinSession(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)

	const sessionID = "session-123"
	dbPath, transcriptPath := newDevinSessionFixture(t, devinSessionRow{
		ID:               sessionID,
		Title:            "DB title wins",
		WorkingDirectory: "/Users/alice/code/my-app",
		Model:            "db-model",
		CreatedAt:        new(int64(1704103199)),
		LastActivityAt:   new(int64(1704103265)),
		WorkspaceJSON:    `{"root_path":"/Users/alice/code/my-app"}`,
		MetadataJSON:     `{"source":"synthetic"}`,
	}, `{
		"title":"Transcript title loses",
		"cwd":"/Users/alice/code/transcript-cwd",
		"created_at":"2024-01-01T10:00:00Z",
		"updated_at":"2024-01-01T10:01:05Z",
		"agent":{"model_name":"devin-1"},
		"final_metrics":{
			"output_tokens":222,
			"input_tokens":100,
			"cache_read_input_tokens":300,
			"cost_usd":99,
			"mystery_tokens":444
		},
		"steps":[
			{"step_id":100,"source":"system","timestamp":"2024-01-01T10:00:00Z","message":"Session booted"},
			{"step_id":"step-1","source":"user","timestamp":"2024-01-01T10:00:01Z","message":"Fix the login bug"},
			{"step_id":"step-skip","source":"user","timestamp":"2024-01-01T10:00:02Z","message":[{"type":"text","text":""},{"type":"unknown","value":"ignored"}]},
			{"step_id":"step-3","source":"agent","timestamp":"2024-01-01T10:00:05Z","message":[{"type":"thinking","thinking":"Check auth flow"},{"type":"text","text":"Inspecting files."},{"type":"tool_use","id":"tool-msg","name":"read_file","input":{"file_path":"README.md"}}],"tool_use":[{"id":"tool-top-1","name":"shell_command","input":{"command":"ls -la"}},{"id":"tool-top-2","name":"edit_file","input":{"path":"main.go"}}]},
			{"step_id":"step-4","source":"user","timestamp":"2024-01-01T10:01:00Z","tool_result":[{"tool_use_id":"tool-top-1","content":"file1\nfile2"}]},
			{"step_id":"step-5","source":"agent","timestamp":"2024-01-01T10:01:05Z","message":[{"type":"unknown","value":"ignored"}],"tool_result":[{"tool_use_id":"tool-top-2","content":[{"type":"text","text":"patch applied"}]}]}
		]
	}`)

	sess, msgs, err := parseDevinSession(t.Context(), dbPath, sessionID, "local")
	require.NoError(err)
	assertSessionMeta(t, sess, "devin:"+sessionID, "my_app", AgentDevin)
	require.Len(msgs, 5)
	assert.Equal(VirtualSourcePath(dbPath, sessionID), sess.File.Path)
	assert.Equal(transcriptPath, filepath.Join(filepath.Dir(dbPath), "transcripts", sessionID+".json"))
	assert.Equal("DB title wins", sess.SessionName)
	assert.Equal("/Users/alice/code/my-app", sess.Cwd)
	assert.Equal("Fix the login bug", sess.FirstMessage)
	assert.Equal(1, sess.UserMessageCount)
	assertTimestamp(t, sess.StartedAt, time.Unix(1_704_103_199, 0).UTC())
	assertTimestamp(t, sess.EndedAt, time.Unix(1_704_103_265, 0).UTC())
	assert.True(sess.HasTotalOutputTokens)
	assert.Equal(222, sess.TotalOutputTokens)
	assert.True(sess.HasPeakContextTokens)
	assert.Equal(400, sess.PeakContextTokens)
	hasTotal, hasPeak := sess.AggregateTokenPresence()
	assert.True(hasTotal)
	assert.True(hasPeak)

	assert.Equal(0, msgs[0].Ordinal)
	assert.Equal(1, msgs[1].Ordinal)
	assert.Equal(3, msgs[2].Ordinal)
	assert.Equal(4, msgs[3].Ordinal)
	assert.Equal(5, msgs[4].Ordinal)

	assert.Equal(RoleSystem, msgs[0].Role)
	assert.True(msgs[0].IsSystem)
	assert.Equal("100", msgs[0].SourceUUID)

	assert.Equal(RoleUser, msgs[1].Role)
	assert.False(msgs[1].IsSystem)
	assert.Equal("Fix the login bug", msgs[1].Content)

	assistant := msgs[2]
	assert.Equal(RoleAssistant, assistant.Role)
	assert.Equal("devin-1", assistant.Model)
	assert.True(assistant.HasThinking)
	assert.True(assistant.HasToolUse)
	assert.Equal("Check auth flow", assistant.ThinkingText)
	assert.Contains(assistant.Content, "[Thinking]\nCheck auth flow\n[/Thinking]")
	assert.Contains(assistant.Content, "Inspecting files.")
	assert.Contains(assistant.Content, "[Read: README.md]")
	assert.Contains(assistant.Content, "[Bash]\n$ ls -la")
	assert.Contains(assistant.Content, "[Edit: main.go]")
	require.Len(assistant.ToolCalls, 3)
	assert.Equal(ParsedToolCall{ToolUseID: "tool-msg", ToolName: "read_file", Category: "Read", InputJSON: `{"file_path":"README.md"}`, Rendering: "[Read: README.md]"}, assistant.ToolCalls[0])
	assert.Equal(ParsedToolCall{ToolUseID: "tool-top-1", ToolName: "shell_command", Category: "Bash", InputJSON: `{"command":"ls -la"}`, Rendering: "[Bash]\n$ ls -la"}, assistant.ToolCalls[1])
	assert.Equal(ParsedToolCall{ToolUseID: "tool-top-2", ToolName: "edit_file", Category: "Edit", InputJSON: `{"path":"main.go"}`, Rendering: "[Edit: main.go]"}, assistant.ToolCalls[2])

	carrier := msgs[3]
	assert.Equal(RoleUser, carrier.Role)
	assert.Empty(carrier.Content)
	require.Len(carrier.ToolResults, 1)
	assert.Equal(ParsedToolResult{ToolUseID: "tool-top-1", ContentLength: len("file1\nfile2"), ContentRaw: `"file1\nfile2"`}, carrier.ToolResults[0])

	standalone := msgs[4]
	assert.Equal(RoleTool, standalone.Role)
	assert.Empty(standalone.Content)
	require.Len(standalone.ToolResults, 1)
	assert.Equal(ParsedToolResult{ToolUseID: "tool-top-2", ContentLength: len("patch applied"), ContentRaw: `[{"type":"text","text":"patch applied"}]`}, standalone.ToolResults[0])
}

func TestParseDevinSessionStepMetricsPopulateTokenUsage(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)

	const sessionID = "session-step-metrics"
	dbPath, _ := newDevinSessionFixture(t, devinSessionRow{
		ID:               sessionID,
		Title:            "Step metrics",
		WorkingDirectory: "/tmp/devin-pricing",
		Model:            "adaptive",
		CreatedAt:        new(int64(1704103199)),
		LastActivityAt:   new(int64(1704103265)),
	}, `{
		"agent":{"model_name":"Adaptive"},
		"final_metrics":{
			"total_completion_tokens":15,
			"total_prompt_tokens":300,
			"total_cached_tokens":20
		},
		"steps":[
			{"step_id":"u1","source":"user","timestamp":"2024-01-01T10:00:01Z","message":"price this"},
			{"step_id":"a1","source":"agent","timestamp":"2024-01-01T10:00:02Z","model_name":"Adaptive","extra":{"generation_model":"glm-5-2"},"metrics":{"prompt_tokens":100,"completion_tokens":10,"cached_tokens":20},"message":"first answer"},
			{"step_id":"a2","source":"agent","timestamp":"2024-01-01T10:00:03Z","model_name":"Adaptive","extra":{"generation_model":"kimi-k2-7"},"metrics":{"prompt_tokens":80,"completion_tokens":5,"cached_tokens":0},"message":"second answer"}
		]
	}`)

	sess, msgs, err := parseDevinSession(t.Context(), dbPath, sessionID, "local")
	require.NoError(err)
	require.Len(msgs, 3)

	first := msgs[1]
	assert.Equal("glm-5-2", first.Model)
	assert.True(first.HasContextTokens)
	assert.True(first.HasOutputTokens)
	assert.Equal(100, first.ContextTokens)
	assert.Equal(10, first.OutputTokens)
	require.NotEmpty(first.TokenUsage)
	assert.Equal(int64(80), gjson.GetBytes(first.TokenUsage, "input_tokens").Int())
	assert.Equal(int64(10), gjson.GetBytes(first.TokenUsage, "output_tokens").Int())
	assert.Equal(int64(20), gjson.GetBytes(first.TokenUsage, "cache_read_input_tokens").Int())

	second := msgs[2]
	assert.Equal("kimi-k2-7", second.Model)
	assert.Equal(80, second.ContextTokens)
	assert.Equal(5, second.OutputTokens)
	require.NotEmpty(second.TokenUsage)
	assert.Equal(int64(80), gjson.GetBytes(second.TokenUsage, "input_tokens").Int())
	assert.Equal(int64(5), gjson.GetBytes(second.TokenUsage, "output_tokens").Int())
	assert.Equal(int64(0), gjson.GetBytes(second.TokenUsage, "cache_read_input_tokens").Int())

	assert.True(sess.HasTotalOutputTokens)
	assert.Equal(15, sess.TotalOutputTokens)
	assert.True(sess.HasPeakContextTokens)
	assert.Equal(100, sess.PeakContextTokens)
}

func TestParseDevinSessionFinalMetricsTotalKeys(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)

	const sessionID = "session-final-total-metrics"
	dbPath, _ := newDevinSessionFixture(t, devinSessionRow{
		ID:               sessionID,
		Title:            "Final metrics",
		WorkingDirectory: "/tmp/devin-final-metrics",
		Model:            "glm-5-2",
		CreatedAt:        new(int64(1704103199)),
		LastActivityAt:   new(int64(1704103265)),
	}, `{
		"agent":{"model_name":"Adaptive"},
		"final_metrics":{
			"total_completion_tokens":15,
			"total_prompt_tokens":300,
			"total_cached_tokens":20
		},
		"steps":[
			{"step_id":"u1","source":"user","timestamp":"2024-01-01T10:00:01Z","message":"summarize"},
			{"step_id":"a1","source":"agent","timestamp":"2024-01-01T10:00:02Z","message":"done"}
		]
	}`)

	sess, msgs, err := parseDevinSession(t.Context(), dbPath, sessionID, "local")
	require.NoError(err)
	require.Len(msgs, 2)
	assert.Empty(msgs[1].TokenUsage)
	assert.True(sess.HasTotalOutputTokens)
	assert.Equal(15, sess.TotalOutputTokens)
	assert.True(sess.HasPeakContextTokens)
	assert.Equal(300, sess.PeakContextTokens)
}

func TestParseDevinSessionTranscriptFallbacks(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)

	const sessionID = "session-fallbacks"
	worktree := filepath.Join(t.TempDir(), "fallback-app")
	require.NoError(os.MkdirAll(filepath.Join(worktree, ".git"), 0o755))
	dbPath, _ := newDevinSessionFixture(t, devinSessionRow{
		ID:             sessionID,
		Model:          "db-model",
		CreatedAt:      new(int64(0)),
		WorkspaceJSON:  fmt.Sprintf(`[{"root_path":%q}]`, worktree),
		MetadataJSON:   `{"mode":"fallback"}`,
		LastActivityAt: nil,
	}, fmt.Sprintf(`{
		"agent":{"model_name":""},
		"workspace_dirs":[{"root_path":%q}],
		"final_metrics":{
			"output_tokens":0,
			"context_tokens":0,
			"total_cost_usd":123
		},
		"steps":[
			{"step_id":"a","source":"user","createdAt":"2024-01-01T10:00:01Z","message":"hi from fallback"},
			{"step_id":"b","source":"agent","updatedAt":"2024-01-01T10:00:05Z","message":[{"type":"text","text":"hello"}]}
		]
	}`, worktree))

	sess, msgs, err := parseDevinSession(t.Context(), dbPath, sessionID, "local")
	require.NoError(err)
	require.Len(msgs, 2)
	assert.Equal("hi from fallback", sess.SessionName)
	assert.Equal("hi from fallback", sess.FirstMessage)
	assert.Equal(worktree, sess.Cwd)
	assert.Equal("fallback_app", sess.Project)
	assert.Equal("db-model", msgs[0].Model)
	assert.Equal("db-model", msgs[1].Model)
	assertTimestamp(t, msgs[0].Timestamp, parseTimestamp(tsEarlyS1))
	assertTimestamp(t, msgs[1].Timestamp, parseTimestamp(tsEarlyS5))
	assertTimestamp(t, sess.StartedAt, parseTimestamp(tsEarlyS1))
	assertTimestamp(t, sess.EndedAt, parseTimestamp(tsEarlyS5))
	hasTotal, hasPeak := sess.AggregateTokenPresence()
	assert.False(hasTotal)
	assert.False(hasPeak)
	assert.False(sess.HasTotalOutputTokens)
	assert.False(sess.HasPeakContextTokens)
}

func TestParseDevinSessionAllowsMissingMetadataTimestamps(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)

	const sessionID = "session-missing-times"
	dbPath, _ := newDevinSessionFixture(t, devinSessionRow{
		ID:               sessionID,
		Title:            "Partial metadata",
		WorkingDirectory: "/tmp/partial",
		Model:            "db-model",
	}, `{
		"steps":[
			{"step_id":"step-1","source":"user","message":"hello"}
		]
	}`)

	sess, msgs, err := parseDevinSession(t.Context(), dbPath, sessionID, "local")
	require.NoError(err)
	require.NotNil(sess)
	require.Len(msgs, 1)

	assert.Equal("devin:"+sessionID, sess.ID)
	assert.True(sess.StartedAt.IsZero())
	assert.True(sess.EndedAt.IsZero())
}

func TestParseDevinSessionEmptyTranscriptUsesDBMetadata(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)

	const sessionID = "session-empty"
	worktree := filepath.Join(t.TempDir(), "db-only-project")
	require.NoError(os.MkdirAll(filepath.Join(worktree, ".git"), 0o755))
	dbPath, _ := newDevinSessionFixture(t, devinSessionRow{
		ID:               sessionID,
		Title:            "DB only session",
		WorkingDirectory: worktree,
		Model:            "db-only-model",
		CreatedAt:        new(int64(1704103200)),
		LastActivityAt:   new(int64(1704103209)),
	}, `{
		"agent":{"model_name":"transcript-model"},
		"steps":[]
	}`)

	sess, msgs, err := parseDevinSession(t.Context(), dbPath, sessionID, "local")
	require.NoError(err)
	require.NotNil(sess)
	assert.Empty(msgs)
	assert.Equal("DB only session", sess.SessionName)
	assert.Equal(worktree, sess.Cwd)
	assert.Equal("db_only_project", sess.Project)
	assert.Equal(0, sess.MessageCount)
	assert.Equal(0, sess.UserMessageCount)
	assertTimestamp(t, sess.StartedAt, time.Unix(1_704_103_200, 0).UTC())
	assertTimestamp(t, sess.EndedAt, time.Unix(1_704_103_209, 0).UTC())
}

func TestParseDevinSessionMissingTranscriptFallsBackToMessageNodes(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)

	const sessionID = "session-db-only"
	fixture := newDevinTestFixture(t, devinSessionRow{
		ID:               sessionID,
		Title:            "DB only session",
		WorkingDirectory: "/tmp/db-only-project",
		Model:            "db-only-model",
		CreatedAt:        new(int64(1704103200)),
		LastActivityAt:   new(int64(1704103209)),
	})
	fixture.insertMessageNodes(t,
		devinSyntheticMessageNodeRow{SessionID: sessionID, NodeID: 1, ChatMessage: `{"role":"user","content":"Recover from SQLite fallback"}`, CreatedAt: 1704103201},
		devinSyntheticMessageNodeRow{SessionID: sessionID, NodeID: 2, ChatMessage: `{"role":"assistant","content":"I'll use the database transcript fallback.","thinking":"checking message_nodes","tool_calls":[{"id":"call-1","function":{"name":"read_file","arguments":"{\"file_path\":\"main.go\"}"}}]}`, CreatedAt: 1704103205},
		devinSyntheticMessageNodeRow{SessionID: sessionID, NodeID: 3, ChatMessage: `{"role":"tool","content":"package main\n","tool_call_id":"call-1"}`, CreatedAt: 1704103207},
	)

	sess, msgs, err := parseDevinSession(t.Context(), fixture.DBPath, sessionID, "local")
	require.NoError(err)
	require.NotNil(sess)
	require.Len(msgs, 3)
	assert.Equal("DB only session", sess.SessionName)
	assert.Equal("Recover from SQLite fallback", sess.FirstMessage)
	assert.Equal(1, sess.UserMessageCount)
	assert.Equal(3, sess.MessageCount)
	assert.Equal("db-only-model", msgs[0].Model)
	assert.Equal(RoleUser, msgs[0].Role)
	assert.Equal("Recover from SQLite fallback", msgs[0].Content)
	assert.Equal(RoleAssistant, msgs[1].Role)
	assert.True(msgs[1].HasThinking)
	assert.True(msgs[1].HasToolUse)
	assert.Contains(msgs[1].Content, "[Thinking]\nchecking message_nodes\n[/Thinking]")
	assert.Contains(msgs[1].Content, "[Read: main.go]")
	require.Len(msgs[1].ToolCalls, 1)
	assert.Equal(ParsedToolCall{ToolUseID: "call-1", ToolName: "read_file", Category: "Read", InputJSON: `{"file_path":"main.go"}`, Rendering: "[Read: main.go]"}, msgs[1].ToolCalls[0])
	assert.Equal(RoleTool, msgs[2].Role)
	require.Len(msgs[2].ToolResults, 1)
	assert.Equal(ParsedToolResult{ToolUseID: "call-1", ContentLength: len("package main\n"), ContentRaw: `"package main\n"`}, msgs[2].ToolResults[0])
}

func TestParseDevinSessionSupportsSessionsTableWithoutMainChainID(t *testing.T) {
	tests := []struct {
		name       string
		transcript string
		wantFirst  string
	}{
		{
			name: "exported transcript",
			transcript: `{
				"steps":[
					{"source":"user","timestamp":"2024-01-01T10:00:01Z","message":"legacy transcript"},
					{"source":"agent","timestamp":"2024-01-01T10:00:02Z","message":"answer"}
				]
			}`,
			wantFirst: "legacy transcript",
		},
		{
			name:      "message nodes fallback",
			wantFirst: "legacy message nodes",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			require := require.New(t)

			const sessionID = "legacy-session"
			root := t.TempDir()
			cliDir := filepath.Join(root, "cli")
			transcriptsDir := filepath.Join(cliDir, "transcripts")
			require.NoError(os.MkdirAll(transcriptsDir, 0o755))
			dbPath := filepath.Join(cliDir, devinDBFilename)

			db, err := sql.Open("sqlite3", dbPath)
			require.NoError(err)
			_, err = db.ExecContext(t.Context(), `
				CREATE TABLE sessions (
					id TEXT PRIMARY KEY,
					title TEXT,
					working_directory TEXT,
					model TEXT,
					created_at INTEGER,
					last_activity_at INTEGER,
					hidden INTEGER NOT NULL DEFAULT 0
				);
				CREATE TABLE message_nodes (
					row_id INTEGER PRIMARY KEY AUTOINCREMENT,
					session_id TEXT NOT NULL,
					node_id INTEGER NOT NULL,
					parent_node_id INTEGER,
					chat_message TEXT NOT NULL,
					created_at INTEGER NOT NULL
				);
				INSERT INTO sessions (
					id, title, working_directory, model,
					created_at, last_activity_at
				) VALUES (
					'legacy-session', 'Legacy session', '/tmp/legacy',
					'legacy-model', 1704103200, 1704103202
				);
			`)
			require.NoError(err)
			if tt.transcript == "" {
				_, err = db.ExecContext(t.Context(), `
					INSERT INTO message_nodes (
						session_id, node_id, chat_message, created_at
					) VALUES
						('legacy-session', 1, '{"role":"user","content":"legacy message nodes"}', 1704103201),
						('legacy-session', 2, '{"role":"assistant","content":"answer"}', 1704103202)
				`)
				require.NoError(err)
			} else {
				require.NoError(os.WriteFile(
					filepath.Join(transcriptsDir, sessionID+".json"),
					[]byte(tt.transcript), 0o644,
				))
			}
			require.NoError(db.Close())

			sess, msgs, err := parseDevinSession(t.Context(), dbPath, sessionID, "local")
			require.NoError(err)
			require.NotNil(sess)
			require.Len(msgs, 2)
			assert.Equal(t, tt.wantFirst, sess.FirstMessage)
		})
	}
}

func TestParseDevinSessionMessageNodesSumTokenMetricsAlongMainChain(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)

	const sessionID = "session-node-metrics"
	fixture := newDevinTestFixture(t, devinSessionRow{
		ID:               sessionID,
		Title:            "Node metrics",
		WorkingDirectory: "/tmp/node-metrics",
		Model:            "claude-opus-4-8-medium",
		CreatedAt:        new(int64(1704103200)),
		LastActivityAt:   new(int64(1704103210)),
		// Main chain leaf is node 4; the chain is 1 -> 2 -> 4. Node 3 is
		// an abandoned retry branching off node 1 and must not be counted.
		MainChainID: new(int64(4)),
	})
	fixture.insertMessageNodes(t,
		devinSyntheticMessageNodeRow{SessionID: sessionID, NodeID: 1, ChatMessage: `{"role":"user","content":"question"}`, CreatedAt: 1704103201},
		devinSyntheticMessageNodeRow{SessionID: sessionID, NodeID: 2, ParentNodeID: new(int64(1)), ChatMessage: `{"role":"assistant","content":"first answer","metadata":{"metrics":{"input_tokens":10,"output_tokens":5,"cache_read_tokens":100,"cache_creation_tokens":20}}}`, CreatedAt: 1704103205},
		devinSyntheticMessageNodeRow{SessionID: sessionID, NodeID: 3, ParentNodeID: new(int64(1)), ChatMessage: `{"role":"assistant","content":"abandoned retry","metadata":{"metrics":{"input_tokens":999,"output_tokens":999,"cache_read_tokens":999,"cache_creation_tokens":999}}}`, CreatedAt: 1704103206},
		devinSyntheticMessageNodeRow{SessionID: sessionID, NodeID: 4, ParentNodeID: new(int64(2)), ChatMessage: `{"role":"assistant","content":"second answer","metadata":{"metrics":{"input_tokens":3,"output_tokens":7,"cache_read_tokens":null,"cache_creation_tokens":50}}}`, CreatedAt: 1704103207},
	)

	sess, msgs, err := parseDevinSession(t.Context(), fixture.DBPath, sessionID, "local")
	require.NoError(err)
	require.NotNil(sess)

	// Only the main chain (nodes 1, 2, 4) is reconstructed; the retry
	// branch (node 3) is dropped entirely.
	require.Len(msgs, 3)
	assert.Equal("question", msgs[0].Content)
	assert.Equal("first answer", msgs[1].Content)
	assert.Equal("second answer", msgs[2].Content)

	first := msgs[1]
	assert.True(first.HasContextTokens)
	assert.True(first.HasOutputTokens)
	assert.Equal(130, first.ContextTokens) // 10 + 100 + 20
	assert.Equal(5, first.OutputTokens)
	require.NotEmpty(first.TokenUsage)
	assert.Equal(int64(10), gjson.GetBytes(first.TokenUsage, "input_tokens").Int())
	assert.Equal(int64(5), gjson.GetBytes(first.TokenUsage, "output_tokens").Int())
	assert.Equal(int64(100), gjson.GetBytes(first.TokenUsage, "cache_read_input_tokens").Int())
	assert.Equal(int64(20), gjson.GetBytes(first.TokenUsage, "cache_creation_input_tokens").Int())

	// A null cache_read_tokens is treated as absent, not zero-present, so
	// the key is omitted from the priced payload.
	second := msgs[2]
	assert.Equal(53, second.ContextTokens) // 3 + 0 + 50
	assert.Equal(7, second.OutputTokens)
	require.NotEmpty(second.TokenUsage)
	assert.Equal(int64(3), gjson.GetBytes(second.TokenUsage, "input_tokens").Int())
	assert.False(gjson.GetBytes(second.TokenUsage, "cache_read_input_tokens").Exists())
	assert.Equal(int64(50), gjson.GetBytes(second.TokenUsage, "cache_creation_input_tokens").Int())

	// Session totals sum output along the main chain and peak the context;
	// node 3's inflated counters never contribute.
	assert.True(sess.HasTotalOutputTokens)
	assert.Equal(12, sess.TotalOutputTokens) // 5 + 7
	assert.True(sess.HasPeakContextTokens)
	assert.Equal(130, sess.PeakContextTokens)
	hasTotal, hasPeak := sess.AggregateTokenPresence()
	assert.True(hasTotal)
	assert.True(hasPeak)
}

func TestParseDevinSessionMessageNodesPreferGenerationModel(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)

	const sessionID = "session-node-genmodel"
	fixture := newDevinTestFixture(t, devinSessionRow{
		ID:               sessionID,
		Title:            "Generation model",
		WorkingDirectory: "/tmp/node-genmodel",
		// Coarse/empty session alias; the concrete model lives per message.
		Model:          "adaptive",
		CreatedAt:      new(int64(1704103200)),
		LastActivityAt: new(int64(1704103210)),
		MainChainID:    new(int64(2)),
	})
	fixture.insertMessageNodes(t,
		devinSyntheticMessageNodeRow{SessionID: sessionID, NodeID: 1, ChatMessage: `{"role":"user","content":"hi"}`, CreatedAt: 1704103201},
		devinSyntheticMessageNodeRow{SessionID: sessionID, NodeID: 2, ParentNodeID: new(int64(1)), ChatMessage: `{"role":"assistant","content":"answer","metadata":{"generation_model":"claude-opus-4-6-thinking","metrics":{"input_tokens":4,"output_tokens":6}}}`, CreatedAt: 1704103205},
	)

	_, msgs, err := parseDevinSession(t.Context(), fixture.DBPath, sessionID, "local")
	require.NoError(err)
	require.Len(msgs, 2)
	// User node has no generation_model, so it falls back to the session model.
	assert.Equal("adaptive", msgs[0].Model)
	// Assistant node's per-message generation_model wins over the session alias.
	assert.Equal("claude-opus-4-6-thinking", msgs[1].Model)
}

func TestParseDevinSessionMessageNodesDanglingMainChainFallsBackToAllNodes(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)

	const sessionID = "session-node-dangling"
	fixture := newDevinTestFixture(t, devinSessionRow{
		ID:               sessionID,
		Title:            "Dangling chain",
		WorkingDirectory: "/tmp/node-dangling",
		Model:            "claude-opus-4-8-medium",
		CreatedAt:        new(int64(1704103200)),
		LastActivityAt:   new(int64(1704103210)),
		// Points at a node that does not exist; parsing must fall back to
		// every node rather than dropping the session.
		MainChainID: new(int64(9999)),
	})
	fixture.insertMessageNodes(t,
		devinSyntheticMessageNodeRow{SessionID: sessionID, NodeID: 1, ChatMessage: `{"role":"user","content":"hi"}`, CreatedAt: 1704103201},
		devinSyntheticMessageNodeRow{SessionID: sessionID, NodeID: 2, ParentNodeID: new(int64(1)), ChatMessage: `{"role":"assistant","content":"answer","metadata":{"metrics":{"input_tokens":4,"output_tokens":6}}}`, CreatedAt: 1704103205},
	)

	sess, msgs, err := parseDevinSession(t.Context(), fixture.DBPath, sessionID, "local")
	require.NoError(err)
	require.Len(msgs, 2)
	assert.True(sess.HasTotalOutputTokens)
	assert.Equal(6, sess.TotalOutputTokens)
	assert.True(sess.HasPeakContextTokens)
	assert.Equal(4, sess.PeakContextTokens)
}

func TestParseDevinSessionMessageNodesMissingParentFallsBackToAllNodes(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)

	const sessionID = "session-node-missing-parent"
	fixture := newDevinTestFixture(t, devinSessionRow{
		ID:               sessionID,
		Title:            "Missing parent",
		WorkingDirectory: "/tmp/node-missing-parent",
		Model:            "claude-opus-4-8-medium",
		CreatedAt:        new(int64(1704103200)),
		LastActivityAt:   new(int64(1704103210)),
		MainChainID:      new(int64(3)),
	})
	fixture.insertMessageNodes(t,
		devinSyntheticMessageNodeRow{SessionID: sessionID, NodeID: 1, ChatMessage: `{"role":"user","content":"hi"}`, CreatedAt: 1704103201},
		devinSyntheticMessageNodeRow{SessionID: sessionID, NodeID: 2, ParentNodeID: new(int64(1)), ChatMessage: `{"role":"assistant","content":"earlier answer","metadata":{"metrics":{"input_tokens":4,"output_tokens":6}}}`, CreatedAt: 1704103205},
		devinSyntheticMessageNodeRow{SessionID: sessionID, NodeID: 3, ParentNodeID: new(int64(9999)), ChatMessage: `{"role":"assistant","content":"orphaned leaf","metadata":{"metrics":{"input_tokens":7,"output_tokens":8}}}`, CreatedAt: 1704103207},
	)

	sess, msgs, err := parseDevinSession(t.Context(), fixture.DBPath, sessionID, "local")
	require.NoError(err)
	require.Len(msgs, 3)
	assert.Equal("hi", msgs[0].Content)
	assert.Equal("earlier answer", msgs[1].Content)
	assert.Equal("orphaned leaf", msgs[2].Content)
	assert.Equal(14, sess.TotalOutputTokens)
	assert.Equal(7, sess.PeakContextTokens)
}

func TestParseDevinSessionTranscriptStillWinsOverMessageNodes(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)

	const sessionID = "session-transcript-wins"
	fixture := newDevinTestFixture(t, devinSessionRow{
		ID:               sessionID,
		Title:            "Transcript wins",
		WorkingDirectory: "/tmp/transcript-wins",
		Model:            "db-model",
		CreatedAt:        new(int64(1704103200)),
		LastActivityAt:   new(int64(1704103209)),
	})
	fixture.writeTranscript(t, sessionID, `{
		"agent":{"model_name":"transcript-model"},
		"steps":[
			{"step_id":"step-1","source":"user","timestamp":"2024-01-01T10:00:01Z","message":"Use transcript"},
			{"step_id":"step-2","source":"agent","timestamp":"2024-01-01T10:00:05Z","message":"Transcript answer"}
		]
	}`)
	fixture.insertMessageNodes(t,
		devinSyntheticMessageNodeRow{SessionID: sessionID, NodeID: 1, ChatMessage: `{"role":"user","content":"Use fallback instead"}`, CreatedAt: 1704103201},
		devinSyntheticMessageNodeRow{SessionID: sessionID, NodeID: 2, ChatMessage: `{"role":"assistant","content":"Fallback answer"}`, CreatedAt: 1704103205},
	)

	sess, msgs, err := parseDevinSession(t.Context(), fixture.DBPath, sessionID, "local")
	require.NoError(err)
	require.Len(msgs, 2)
	assert.Equal("Use transcript", sess.FirstMessage)
	assert.Equal("transcript-model", msgs[0].Model)
	assert.Equal("Use transcript", msgs[0].Content)
	assert.Equal("Transcript answer", msgs[1].Content)
}

func TestParseDevinSessionMissingTranscriptWithoutDBMessagesReturnsRedactedError(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)

	const sessionID = "session-db-only-empty"
	fixture := newDevinTestFixture(t, devinSessionRow{
		ID:               sessionID,
		Title:            "DB only session",
		WorkingDirectory: "/tmp/db-only-project",
		Model:            "db-only-model",
		CreatedAt:        new(int64(1704103200)),
		LastActivityAt:   new(int64(1704103209)),
	})

	sess, msgs, err := parseDevinSession(t.Context(), fixture.DBPath, sessionID, "local")
	require.Nil(sess)
	assert.Nil(msgs)
	require.Error(err)
	assert.ErrorContains(err, "missing devin transcript")
	assert.ErrorContains(err, devinRedactedTranscriptPath())
	assert.ErrorContains(err, devinRedactedSessionID())
	assert.NotContains(err.Error(), sessionID)
}

func TestParseDevinSessionFallbackErrorsStayRedacted(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)

	const (
		sessionID      = "secret-session"
		secretSentinel = "oauth-token-SYNTHETIC-SECRET-SENTINEL"
	)
	fixture := newDevinTestFixture(t, devinSessionRow{
		ID:               sessionID,
		Title:            "DB only session",
		WorkingDirectory: "/tmp/db-only-project",
		Model:            "db-only-model",
		CreatedAt:        new(int64(1704103200)),
		LastActivityAt:   new(int64(1704103209)),
	})
	fixture.insertMessageNodes(t,
		devinSyntheticMessageNodeRow{SessionID: sessionID, NodeID: 1, ChatMessage: `{"content":"` + secretSentinel, CreatedAt: 1704103201},
	)

	sess, msgs, err := parseDevinSession(t.Context(), fixture.DBPath, sessionID, "local")
	require.Nil(sess)
	assert.Nil(msgs)
	require.Error(err)
	assert.ErrorContains(err, "missing devin transcript")
	assert.ErrorContains(err, devinRedactedTranscriptPath())
	assert.ErrorContains(err, devinRedactedSessionID())
	assert.NotContains(err.Error(), sessionID)
	assert.NotContains(err.Error(), secretSentinel)
}

func TestParseDevinSessionCorruptTranscriptReturnsRedactedError(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)

	const sessionID = "secret-session"
	dbPath, transcriptPath := newDevinSessionFixture(t, devinSessionRow{
		ID:               sessionID,
		Title:            "Corrupt transcript",
		WorkingDirectory: "/tmp/app",
		Model:            "db-model",
		CreatedAt:        new(int64(1704103199)),
		LastActivityAt:   new(int64(1704103265)),
	}, `{"steps":[]}`)
	require.NoError(os.WriteFile(transcriptPath, []byte(`{"apiKey":"secret-value","steps":[`), 0o644))

	sess, msgs, err := parseDevinSession(t.Context(), dbPath, sessionID, "local")
	require.Nil(sess)
	assert.Nil(msgs)
	require.Error(err)
	assert.ErrorContains(err, "invalid devin transcript")
	assert.ErrorContains(err, devinRedactedTranscriptPath())
	assert.ErrorContains(err, devinRedactedSessionID())
	assert.NotContains(err.Error(), transcriptPath)
	assert.NotContains(err.Error(), sessionID)
	assert.NotContains(err.Error(), "secret-value")
}

func TestDevinTranscriptPathErrorStaysRedacted(t *testing.T) {
	assert := assert.New(t)

	const sessionID = "secret-session-id"
	secretPath := filepath.Join(t.TempDir(), "cli", "transcripts", sessionID+".json")

	err := newDevinTranscriptError("read", &os.PathError{
		Op:   "open",
		Path: secretPath,
		Err:  os.ErrPermission,
	})

	require.Error(t, err)
	assert.ErrorContains(err, "read devin transcript")
	assert.ErrorContains(err, devinRedactedTranscriptPath())
	assert.ErrorContains(err, devinRedactedSessionID())
	assert.ErrorContains(err, "permission denied")
	assert.NotContains(err.Error(), secretPath)
	assert.NotContains(err.Error(), sessionID)
}

func TestParseDevinSessionRedactsCredentialPathsAndTokenLikeValues(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)

	const (
		sessionID      = "session-privacy"
		secretSentinel = "oauth-token-SYNTHETIC-SECRET-SENTINEL"
	)
	fixture := newDevinTestFixture(t,
		devinSessionRow{ID: sessionID, Title: "Privacy", WorkingDirectory: "/tmp/app", Model: "db-model", CreatedAt: new(int64(1704103199)), LastActivityAt: new(int64(1704103265))},
	)
	transcriptPath := fixture.writeTranscript(t, sessionID, `{"access_token":"oauth-token-SYNTHETIC-SECRET-SENTINEL","steps":[`)

	secretRoot := filepath.Join(t.TempDir(), secretSentinel, "config", "mcp", "oauth", "devin-root")
	require.NoError(os.MkdirAll(filepath.Dir(secretRoot), 0o755))
	require.NoError(os.Rename(fixture.Root, secretRoot))
	dbPath := filepath.Join(secretRoot, "cli", devinDBFilename)

	sess, msgs, err := parseDevinSession(t.Context(), dbPath, sessionID, "local")
	require.Nil(sess)
	assert.Nil(msgs)
	assert.ErrorContains(err, "invalid devin transcript")
	assert.ErrorContains(err, devinRedactedTranscriptPath())
	assert.ErrorContains(err, devinRedactedSessionID())
	assertDevinErrorRedacted(t, err,
		secretSentinel,
		"mcp/oauth",
		"config",
		"access_token",
		transcriptPath,
	)
}
