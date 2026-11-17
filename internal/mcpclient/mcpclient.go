//  Copyright (c) 2026, R.I. Pienaar and the Choria Project contributors
//
//  SPDX-License-Identifier: Apache-2.0

// Package mcpclient imports the tools of a third-party MCP server into an agent
// run. It is the policy and transport layer over the client half of the Model
// Context Protocol SDK: it builds a transport from an mcp_clients entry,
// resolves that entry's "${VAR}" references against the process environment and
// its "${file:PATH}" references against the filesystem,
// connects one session per server and holds those sessions for the caller to
// reach by name and close when the run ends. Over those sessions it lists each
// server's tools, applies the entry's include and exclude filters, names each
// survivor "<alias>_<tool>", and builds it as a functool.Tool the model calls like
// any other.
//
// It is the counterpart of internal/remotetools, which is the same layer for the
// tools an agent imports from a2a peers, and it copies that package's shape:
// what a server offers is returned as data rather than printed, and errors are
// returned rather than logged, so the same code serves both an agent run, which
// treats an unreachable server as fatal, and fisk info, which reports it and
// carries on.
package mcpclient

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"os"
	"slices"
	"strings"
	"sync"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/choria-io/fisk-ai/config"
	"github.com/choria-io/fisk-ai/internal/telemetry"
)

// Dialer builds the transport for one configured server. Sessions uses the
// transports this package builds from the entry itself, over stdio for a server
// with a command and streamable HTTP for one with a url, unless Options.Dialer
// supplies a Dialer of its own. An embedder speaking a transport this package
// does not build, and a test driving a server in the same process over
// mcp.NewInMemoryTransports, supply one.
//
// A transport is connected exactly once, so a Dialer that may be asked twice for
// the same server, as it is when a session is replaced, returns a new transport
// each time.
type Dialer func(ctx context.Context, server config.MCPServer) (mcp.Transport, error)

// Options configures the sessions Connect opens.
type Options struct {
	// Servers are the MCP servers to connect, in the order they were configured.
	// Each name is connected once, so two entries sharing a name are an error.
	Servers []config.MCPServer
	// Identity is the client name each server is told at initialize, the way
	// mcpserver.Options.Name identifies this process to the clients it serves. Pass
	// the agent's identity.
	Identity string
	// Version is the client version each server is told at initialize. Pass the
	// build version.
	Version string
	// CredentialEnvNames are the operator-named environment variables holding a
	// credential, which are removed from the environment of a stdio child on top of
	// the ones every llm provider linked into the build declares. Pass
	// config.Config.CredentialEnvNames.
	CredentialEnvNames []string
	// LookupEnv resolves the "${VAR}" references in an entry's env, headers and url.
	// Nil reads the process environment through os.LookupEnv.
	LookupEnv func(name string) (string, bool)
	// ReadFile resolves the "${file:PATH}" references in an entry's env, headers and
	// url. Nil reads the file through config.ReadCredentialFile, with a relative path
	// resolved under WorkDir, so a Docker Compose secret at /run/secrets/<name> is
	// read as written and trailing whitespace is dropped.
	ReadFile func(path string) (string, error)
	// WorkDir is the directory a stdio child is started in, and is what makes a
	// command written with a separator ("./bin/server") resolve against it rather than
	// against the process working directory. Empty starts the child in the process
	// working directory. Pass config.Config.RootDirectory. A relative "${file:PATH}"
	// in any entry, stdio or HTTP, resolves under it too; an HTTP server itself runs
	// wherever its operator started it.
	WorkDir string
	// Dialer overrides how a transport is built for a server. Nil builds the stdio
	// and streamable HTTP transports this package builds from the entry.
	Dialer Dialer
}

// Sessions holds one live MCP session per configured server. It is safe for
// concurrent use: one Sessions backs every run a server process hosts, and the
// tools imported from it are called from many runs at once.
//
// Sessions owns the sessions it opens. A session that has ended cannot be
// revived, so a caller reaches its session through Use for each call rather than
// holding the mcp.ClientSession, which Sessions may have replaced since.
type Sessions struct {
	opts    Options
	names   []string
	entries map[string]*entry

	mu        sync.Mutex
	closed    bool
	listeners []listener
	nextID    uint64
}

// listener is one registered tool-list watcher and the id OnToolListChanged's
// cancel removes it by.
type listener struct {
	id uint64
	fn func(ToolListChange)
}

// entry is one configured server and the session currently connected to it. Its
// own mutex guards the session, so replacing one server's session leaves calls
// to every other server running.
type entry struct {
	server config.MCPServer
	// secrets are the values this server's url references resolve to, replaced in
	// every error this package returns for it.
	secrets []string

	mu      sync.Mutex
	session *mcp.ClientSession
	// done is closed when session ends, whether the server went away, the child
	// died, or Close closed it.
	done chan struct{}

	// watchMu guards the two flags below, which keep one server's rebuilds to one
	// at a time.
	watchMu sync.Mutex
	// rebuilding says a goroutine is re-listing this server now.
	rebuilding bool
	// again says a notification arrived while that goroutine was working, so it
	// lists once more when it is done rather than answering with what it read
	// before the change.
	again bool
}

// Connect opens a session with every configured server, in the order they were
// configured, and returns them keyed by name. Each server gets its own
// StartupTimeout to be started or reached and to finish the initialize
// handshake, and its "${VAR}" and "${file:PATH}" references are resolved here
// rather than when the config was parsed, so a variable that is not set or a file
// that cannot be read fails this call naming the variable or the path and the
// server.
//
// What the references in a url resolve to is kept for the life of the sessions and
// replaced with "REDACTED" in every error they return, so a credential a service
// takes in the path rather than in the query is not printed either.
//
// A server that cannot be connected fails the call: the sessions already opened
// are closed, so a failed Connect leaves nothing running. The error is returned
// rather than logged, because whether an unreachable server is fatal is the
// caller's decision.
//
// The caller owns the returned Sessions and must Close it, which ends every
// session and stops every stdio child it started.
func Connect(ctx context.Context, opts Options) (*Sessions, error) {
	if opts.LookupEnv == nil {
		opts.LookupEnv = os.LookupEnv
	}
	if opts.ReadFile == nil {
		opts.ReadFile = credentialFileReader(opts.WorkDir)
	}

	s := &Sessions{
		opts:    opts,
		names:   make([]string, 0, len(opts.Servers)),
		entries: make(map[string]*entry, len(opts.Servers)),
	}

	for _, server := range opts.Servers {
		_, dup := s.entries[server.Name]
		if dup {
			_ = s.closeOpened(ctx)
			return nil, fmt.Errorf("mcp server %q is configured more than once", server.Name)
		}

		e := &entry{server: server, secrets: serverSecrets(server, opts.LookupEnv, opts.ReadFile)}
		err := s.open(ctx, e)
		if err != nil {
			_ = s.closeOpened(ctx)
			return nil, err
		}

		s.entries[server.Name] = e
		s.names = append(s.names, server.Name)
	}

	return s, nil
}

// closeOpened ends the sessions a failed Connect had already opened. It waits however
// long the children take rather than under the caller's context, which is often
// already expired: the server that failed took its whole startup timeout to do it, and
// a caller told the connect failed is entitled to a process with no children left in
// it.
func (s *Sessions) closeOpened(ctx context.Context) error {
	return s.Close(context.WithoutCancel(ctx))
}

// Names are the configured server names, in the order they were configured.
func (s *Sessions) Names() []string {
	return slices.Clone(s.names)
}

// CheckServers reports whether these sessions were opened for exactly the servers
// in want, naming what differs when they were not.
//
// The import walks the list these sessions carry rather than the caller's
// configuration, so a set opened from a different configuration would import its
// own servers, under its own aliases and filters, in a run that never asked for
// them. A caller that injects sessions calls this with the servers its
// configuration declares and refuses the run on an error, rather than taking the
// substitution.
//
// Only the names are compared. An alias, a filter and a transport belong to
// whoever opened the session, and a borrower has no standing to overrule them.
func (s *Sessions) CheckServers(want []config.MCPServer) error {
	opened := make(map[string]bool, len(s.names))
	for _, name := range s.names {
		opened[name] = true
	}

	wanted := make(map[string]bool, len(want))
	var unconnected []string
	for _, server := range want {
		wanted[server.Name] = true
		if !opened[server.Name] {
			unconnected = append(unconnected, server.Name)
		}
	}

	var unconfigured []string
	for _, name := range s.names {
		if !wanted[name] {
			unconfigured = append(unconfigured, name)
		}
	}

	if len(unconnected) == 0 && len(unconfigured) == 0 {
		return nil
	}

	var parts []string
	if len(unconnected) > 0 {
		parts = append(parts, fmt.Sprintf("configured but not connected: %s", strings.Join(unconnected, ", ")))
	}
	if len(unconfigured) > 0 {
		parts = append(parts, fmt.Sprintf("connected but not configured: %s", strings.Join(unconfigured, ", ")))
	}

	return fmt.Errorf("the mcp sessions were opened for different servers than this configuration declares (%s)", strings.Join(parts, "; "))
}

// configured are the entries the sessions were opened for, in the order they were
// configured, for the import to read each one's alias and filters.
func (s *Sessions) configured() []config.MCPServer {
	s.mu.Lock()
	defer s.mu.Unlock()

	out := make([]config.MCPServer, 0, len(s.names))
	for _, name := range s.names {
		out = append(out, s.entries[name].server)
	}

	return out
}

// Use calls fn with the live session for the named server. It is how a tool
// handler reaches its server: a session that has ended is replaced first, so fn
// is always given one that was live when it was handed over, which a caller
// holding a session value from an earlier call would not be.
//
// fn's error comes back with this server's credentials redacted, which is where a
// transport error quoting the endpoint it dialed is caught, and errors.Is and
// errors.As still see through it to what fn returned. An unknown name, a session that
// cannot be replaced, and a Sessions that has been closed are reported without calling
// fn. A Close that lands while the call is in flight is reported the same way.
func (s *Sessions) Use(ctx context.Context, name string, fn func(session *mcp.ClientSession) error) error {
	session, err := s.live(ctx, name)
	if err != nil {
		return err
	}

	return redacted(fn(session), s.secrets(name))
}

// secrets are the credential values of the named server, and none for a name that is
// not configured.
func (s *Sessions) secrets(name string) []string {
	s.mu.Lock()
	defer s.mu.Unlock()

	e, known := s.entries[name]
	if !known {
		return nil
	}

	return e.secrets
}

// Close ends every session, which closes the stdin of each stdio child and gives it
// the SDK's terminate window to exit before it is signaled. It is idempotent, and
// every session is closed even when one of them reports a failure.
//
// The sessions are closed at the same time and ctx limits how long Close waits for
// them to finish. A stdio child that ignores its stdin closing costs the SDK's whole
// terminate window, three waits of five seconds, and closing one server after another
// would make a process serving several pay that once per server. When ctx ends first,
// Close returns naming the servers it did not see finish; those closes carry on and
// reap their children, so a caller whose process is about to exit passes a context
// that does not end.
func (s *Sessions) Close(ctx context.Context) error {
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return nil
	}
	s.closed = true
	entries := make([]*entry, 0, len(s.entries))
	for _, name := range s.names {
		entries = append(entries, s.entries[name])
	}
	s.mu.Unlock()

	type closed struct {
		name string
		err  error
	}

	done := make(chan closed, len(entries))
	pending := make(map[string]bool, len(entries))
	for _, e := range entries {
		pending[e.server.Name] = true

		go func(e *entry) {
			// Waiting on the entry lock stops a session opened by a concurrent
			// replacement from outliving Close: that replacement either sees the closed
			// flag and refuses, or completes and is closed here.
			e.mu.Lock()
			defer e.mu.Unlock()

			if e.session == nil {
				done <- closed{name: e.server.Name}
				return
			}

			err := e.session.Close()
			e.session = nil
			if err != nil {
				err = redacted(fmt.Errorf("closing the session with mcp server %q: %w", e.server.Name, err), e.secrets)
			}

			done <- closed{name: e.server.Name, err: err}
		}(e)
	}

	var errs []error
	for len(pending) > 0 {
		select {
		case c := <-done:
			delete(pending, c.name)
			if c.err != nil {
				errs = append(errs, c.err)
			}

		case <-ctx.Done():
			names := slices.Sorted(maps.Keys(pending))
			errs = append(errs, fmt.Errorf("the sessions with mcp servers %s were still closing: %w", strings.Join(names, ", "), ctx.Err()))

			return errors.Join(errs...)
		}
	}

	return errors.Join(errs...)
}

// live returns the session for the named server, replacing one that has ended. The
// ended session is closed as it is replaced, so nothing the SDK attached to it
// outlives it.
func (s *Sessions) live(ctx context.Context, name string) (*mcp.ClientSession, error) {
	s.mu.Lock()
	closed := s.closed
	e, known := s.entries[name]
	s.mu.Unlock()

	if closed {
		return nil, fmt.Errorf("the sessions with the configured mcp servers are closed")
	}
	if !known {
		return nil, fmt.Errorf("no mcp server named %q is configured", name)
	}

	e.mu.Lock()
	defer e.mu.Unlock()

	// Reading the closed flag again under the entry lock catches a Close that
	// landed since the read above: Close sets the flag before it takes this lock,
	// so a Close that has begun is seen here, and one that has not begun cannot
	// clear the session until this call releases the lock. The done channel does
	// not catch it alone, because Close clears the session while the goroutine
	// that closes the channel is still waiting for the session to end.
	s.mu.Lock()
	closed = s.closed
	s.mu.Unlock()
	if closed {
		return nil, fmt.Errorf("the sessions with the configured mcp servers are closed")
	}

	select {
	case <-e.done:
	default:
		if e.session != nil {
			return e.session, nil
		}
	}

	// The entry gives up the ended session before it is handed to the closer, so a
	// failed replacement leaves the entry holding nothing rather than a session
	// somebody else is closing.
	ended := e.session
	e.session = nil
	closeEnded(ended)

	err := s.open(ctx, e)
	if err != nil {
		return nil, err
	}

	return e.session, nil
}

// closeEnded closes a session that has ended and whose replacement is being opened.
//
// The session is closed rather than dropped because Close is what releases what the
// SDK hung off it. Under the 2026-07-28 protocol every session opens a
// subscriptions/listen stream whose watcher waits on a context derived from
// context.Background(), and Close is the only thing that cancels it, so a dropped
// session parks that goroutine for the life of the process, once per reconnect. A
// stdio child is reaped in the same call: nothing else waits on the command.
//
// It runs on a goroutine of its own because the caller holds the entry lock and is
// here to make a call. Closing a stdio child that ignores its stdin closing costs the
// SDK's whole terminate window, three waits of five seconds, and a synchronous close
// would hold every call to this server, the replacement included, for that long. The
// close error is dropped for the same reason it is not the caller's: it describes
// tearing down a session that had already ended, where the caller wants its answer or
// the failure that brought it here.
func closeEnded(session *mcp.ClientSession) {
	if session == nil {
		return
	}

	go func() { _ = session.Close() }()
}

// open connects a session for e and records it. The caller holds e's lock, except
// in Connect where e is not yet reachable.
//
// The context it connects under carries the entry's startup timeout, and is
// canceled as soon as the handshake is done: the SDK detaches the session from
// it, so the deadline limits the connect and not the life of the session. The
// import applies the same timeout again when it lists the server's tools.
//
// It is spanned when ctx carries a telemetry provider, which covers the connects a
// run makes at startup and a session replaced during a tool call, and leaves the
// rebuild after a tools/list_changed notification untraced: that runs on a goroutine
// of these sessions' own, on a context belonging to no run.
func (s *Sessions) open(ctx context.Context, e *entry) error {
	ctx, span := telemetry.ProviderFromContext(ctx).StartMCPConnect(ctx, s.spanInfo(e.server))

	ctx, cancel := context.WithTimeout(ctx, e.server.StartupTimeout())
	defer cancel()

	transport, err := s.transport(ctx, e.server)
	if err != nil {
		// The transport is built from the entry alone, so a failure here is the
		// configuration: a variable that is not set, a url that will not parse, an entry
		// naming neither a command nor a url.
		span.Finish(telemetry.MCPServerOutcome{Failed: true, Class: telemetry.ClassConfig})
		return redacted(err, e.secrets)
	}

	session, err := s.client(e).Connect(ctx, transport, nil)
	if err != nil {
		span.Finish(telemetry.MCPServerOutcome{Failed: true, Class: connectClass(err)})

		if errors.Is(err, context.DeadlineExceeded) {
			return redacted(fmt.Errorf("connecting to mcp server %q: it did not answer the initialize handshake within %v: %w", e.server.Name, e.server.StartupTimeout(), err), e.secrets)
		}

		return redacted(fmt.Errorf("connecting to mcp server %q: %w", e.server.Name, err), e.secrets)
	}

	span.Finish(telemetry.MCPServerOutcome{})

	done := make(chan struct{})
	go func() {
		_ = session.Wait()
		close(done)
	}()

	e.session = session
	e.done = done

	return nil
}

// client builds the MCP client for one server. Each server gets its own, rather
// than sharing one across all of them, because the handlers a client answers with
// (elicitation, the tool-list notification) are fixed on mcp.ClientOptions before
// Connect, so one shared client would make every such choice global to the
// process instead of per server.
//
// The client advertises nothing. Passing an empty mcp.ClientCapabilities drops
// the roots capability the SDK advertises by default, along with its listChanged:
// what goes on the wire is the empty object, on both the stateless server/discover
// path and the legacy initialize handshake. Reading mcp.ClientCapabilities alone
// suggests otherwise, since its deprecated Roots field is a non-pointer struct
// that encoding/json will not omit, but that field never reaches the wire: the
// SDK marshals capabilities through an unexported wrapper whose own roots field,
// a nil pointer at the shallower depth, shadows it (see #607 in the SDK). Both
// paths are pinned by a test that reads the bytes the client writes, so a change
// in the SDK is caught rather than assumed.
//
// No sampling handler is set, so a foreign server cannot spend this agent's model
// budget, and no elicitation handler, so nothing is advertised that no one here
// answers yet.
//
// The tool-list handler is set only for a server whose entry sets WatchTools, and it
// is set whether or not anything is watching yet: under the 2026-07-28 protocol
// Connect subscribes to tools/list_changed only when the handler is there and the
// server advertises tools.listChanged, opening the subscriptions/listen stream with
// the notifications the client's handlers ask for, and a session connected without
// one is never told. Against a server that does not advertise it, WatchTools has no
// effect. A run that registers with
// OnToolListChanged after the sessions were built, which is every run under fisk
// serve, would otherwise have nothing to hear.
//
// Without WatchTools no such stream is opened, and the list read at connect stands
// for the life of the session. That is the default because the stream is long lived
// and the SDK reconnects it: a server that answers server/discover and then refuses
// the resumption fails the whole session, so a run against it starts with none of
// its tools.
func (s *Sessions) client(e *entry) *mcp.Client {
	opts := &mcp.ClientOptions{Capabilities: &mcp.ClientCapabilities{}}
	if e.server.WatchTools {
		opts.ToolListChangedHandler = func(context.Context, *mcp.ToolListChangedRequest) {
			s.toolListChanged(e)
		}
	}

	return mcp.NewClient(&mcp.Implementation{Name: s.opts.Identity, Version: s.opts.Version}, opts)
}

// OnToolListChanged registers fn to hear that a server changed its tool list, with
// that server's list as it stands after the change. It returns the function that
// removes the registration, which a caller must call when it stops caring: sessions
// injected into a run outlive it, so a run that left its registration behind would
// keep rebuilding a tool set nobody reads.
//
// fn is called on a goroutine of these sessions', never on the run's, and the
// registrations are called one after another in the order they were made. It is
// called with the entry's include and exclude filters already applied, and with Err
// set when the server could not be re-listed, which leaves the caller holding the
// tools it already had.
//
// A notification that arrives while no one is registered is dropped without a round
// trip: the list is read when a run starts, so nothing is lost by not reading it
// while no run is watching.
//
// A registration hears from the servers whose entries set WatchTools and from no
// others, since only those sessions subscribe to tool-list changes.
func (s *Sessions) OnToolListChanged(fn func(ToolListChange)) func() {
	s.mu.Lock()
	defer s.mu.Unlock()

	s.nextID++
	id := s.nextID
	s.listeners = append(s.listeners, listener{id: id, fn: fn})

	return func() {
		s.mu.Lock()
		defer s.mu.Unlock()

		s.listeners = slices.DeleteFunc(s.listeners, func(l listener) bool { return l.id == id })
	}
}

// watchers are the registrations to call, in the order they were made.
func (s *Sessions) watchers() []listener {
	s.mu.Lock()
	defer s.mu.Unlock()

	return slices.Clone(s.listeners)
}

// toolListChanged handles one server's tools/list_changed notification.
//
// The work happens on a goroutine of its own rather than on the SDK's: the handler
// is called on the session's incoming-message path, which delivers notifications
// one at a time, so re-listing the server from inside it would hold up everything
// else the server has to say for a whole round trip.
//
// A server is re-listed one notification at a time. One arriving while a listing is
// running neither starts a second listing nor is dropped: the running one lists again
// when it is done, since what it read came from before that change.
func (s *Sessions) toolListChanged(e *entry) {
	e.watchMu.Lock()
	if e.rebuilding {
		e.again = true
		e.watchMu.Unlock()
		return
	}
	e.rebuilding = true
	e.watchMu.Unlock()

	go func() {
		for {
			s.notifyToolListChange(e)

			e.watchMu.Lock()
			if !e.again {
				e.rebuilding = false
				e.watchMu.Unlock()
				return
			}
			e.again = false
			e.watchMu.Unlock()
		}
	}()
}

// notifyToolListChange re-lists one server and hands the result to everyone
// registered.
//
// The listing runs on a context of its own rather than the notification's, which
// belongs to the session. The entry's startup timeout limits it, as it limits the
// listing an import makes.
func (s *Sessions) notifyToolListChange(e *entry) {
	watchers := s.watchers()
	if len(watchers) == 0 {
		return
	}

	change := ToolListChange{Server: e.server}
	change.Kept, change.Discovered, change.RTT, change.Err = listServer(context.Background(), s, e.server)

	for _, w := range watchers {
		w.fn(change)
	}
}
