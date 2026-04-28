package cmd

// Shared test fixtures for cmd-package tests. mockRunner and setEnvCall used
// to live in cmd/init_test.go; QUM-346 deleted that file as part of the
// tmux-mode parent entrypoint removal, so the helpers were hoisted here for
// the remaining tests (cmd/spawn_test.go, cmd/color_test.go) that still need
// a tmux.Runner stub for child-spawn coverage.

// mockRunner implements tmux.Runner for testing.
type mockRunner struct {
	hasSession            bool
	newSessionErr         error
	newSessionWithWinErr  error
	attachErr             error
	newSessionName        string
	newSessionEnv         map[string]string
	newSessionCmd         string
	newSessionWithWinName string
	newSessionWithWinWin  string
	newSessionWithWinEnv  map[string]string
	newSessionWithWinCmd  string
	attachCalled          bool
	attachName            string
	sourceFileCalled      bool
	sourceFileSession     string
	sourceFilePath        string
	setEnvCalls           []setEnvCall
	setEnvAfterNewSession bool
}

type setEnvCall struct {
	Session string
	Key     string
	Value   string
}

func (m *mockRunner) HasWindow(string, string) bool { return false }

func (m *mockRunner) HasSession(name string) bool {
	return m.hasSession
}

func (m *mockRunner) NewSession(name string, env map[string]string, shellCmd string) error {
	m.newSessionName = name
	m.newSessionEnv = env
	m.newSessionCmd = shellCmd
	return m.newSessionErr
}

func (m *mockRunner) NewSessionWithWindow(sessionName, windowName string, env map[string]string, shellCmd string) error {
	m.newSessionWithWinName = sessionName
	m.newSessionWithWinWin = windowName
	m.newSessionWithWinEnv = env
	m.newSessionWithWinCmd = shellCmd
	return m.newSessionWithWinErr
}

func (m *mockRunner) NewWindow(sessionName, windowName string, env map[string]string, shellCmd string) error {
	return nil
}

func (m *mockRunner) KillWindow(sessionName, windowName string) error {
	return nil
}

func (m *mockRunner) ListWindowPIDs(sessionName, windowName string) ([]int, error) {
	return nil, nil
}

func (m *mockRunner) ListSessionNames() ([]string, error) { return nil, nil }

func (m *mockRunner) SendKeys(sessionName, windowName string, keys string) error {
	return nil
}

func (m *mockRunner) Attach(name string) error {
	m.attachCalled = true
	m.attachName = name
	return m.attachErr
}

func (m *mockRunner) SourceFile(sessionName, filePath string) error {
	m.sourceFileCalled = true
	m.sourceFileSession = sessionName
	m.sourceFilePath = filePath
	return nil
}

func (m *mockRunner) SetEnvironment(sessionName, key, value string) error {
	if m.newSessionWithWinName != "" {
		m.setEnvAfterNewSession = true
	}
	m.setEnvCalls = append(m.setEnvCalls, setEnvCall{Session: sessionName, Key: key, Value: value})
	return nil
}
