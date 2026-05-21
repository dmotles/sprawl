// Package supervisor: this file declares QUM-613 named sub-interfaces for
// capabilities that are optional on RuntimeHandle. They replace previously
// inline `interface{ Foo() ... }` duck-typed assertions scattered through
// runtime.go so the protocol between AgentRuntime and concrete handle types
// is named, greppable, and compile-time enforced for production handles.
//
// The existing named sub-interfaces `runtimeHandleDone` and
// `unifiedRuntimeProvider` live in runtime.go and follow the same pattern
// (unexported, package-local).
package supervisor

// terminalFaultProbe is satisfied by RuntimeHandle implementations whose
// backend session exposes sticky-fault state. Satisfied by *unifiedHandle,
// *WeaveRuntimeHandle, *runtimeTestSession, *fakeBackendSession.
type terminalFaultProbe interface {
	IsTerminallyFaulted() bool
}

// stopWaitTimeoutProbe is satisfied by RuntimeHandle implementations that
// surface whether the most recent Stop's bounded session.Wait() timed out.
// Satisfied by *unifiedHandle, *runtimeTestSession. WeaveRuntimeHandle
// deliberately does not implement this: weave teardown skips session.Wait
// so there is no timeout to report.
type stopWaitTimeoutProbe interface {
	StopWaitTimedOut() bool
}

// autonomousTurnProbe is satisfied by RuntimeHandle implementations whose
// backend session can report whether it is currently between sprawl-initiated
// turns. Satisfied by *unifiedHandle, *WeaveRuntimeHandle,
// *fakeBackendSession, *fakeInAutonomousTurnHandle.
type autonomousTurnProbe interface {
	InAutonomousTurn() bool
}

// terminalFaultInjectorProbe is a test-only seam exposed by handle types
// that allow forcing the underlying session into a terminally-faulted
// state. Satisfied by *unifiedHandle, *fakeBackendSession. Not implemented
// by WeaveRuntimeHandle.
type terminalFaultInjectorProbe interface {
	InduceTerminalFault(error)
}

// Compile-time enforcement: production handle types must keep satisfying
// the probes they currently expose. Tests cover additional doubles.
var (
	_ terminalFaultProbe         = (*unifiedHandle)(nil)
	_ terminalFaultProbe         = (*WeaveRuntimeHandle)(nil)
	_ stopWaitTimeoutProbe       = (*unifiedHandle)(nil)
	_ autonomousTurnProbe        = (*unifiedHandle)(nil)
	_ autonomousTurnProbe        = (*WeaveRuntimeHandle)(nil)
	_ terminalFaultInjectorProbe = (*unifiedHandle)(nil)
)
