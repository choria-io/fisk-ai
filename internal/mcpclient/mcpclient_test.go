//  Copyright (c) 2026, R.I. Pienaar and the Choria Project contributors
//
//  SPDX-License-Identifier: Apache-2.0

package mcpclient

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/modelcontextprotocol/go-sdk/jsonrpc"
	"github.com/modelcontextprotocol/go-sdk/mcp"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/choria-io/fisk-ai/config"
)

var _ = Describe("Sessions", func() {
	var (
		ctx     context.Context
		cancel  context.CancelFunc
		servers *fakeServers
	)

	BeforeEach(func() {
		ctx, cancel = context.WithTimeout(context.Background(), 30*time.Second)
		DeferCleanup(cancel)

		servers = newFakeServers()
	})

	connected := func(entries ...config.MCPServer) *Sessions {
		GinkgoHelper()

		sessions, err := Connect(ctx, Options{
			Servers:  entries,
			Identity: "fisk-test",
			Version:  "0.0.1",
			Dialer:   servers.dialer(),
		})
		Expect(err).ToNot(HaveOccurred())
		DeferCleanup(func() { Expect(sessions.Close(ctx)).To(Succeed()) })

		return sessions
	}

	Describe("Connect", func() {
		It("should connect every configured server and reach each by name", func() {
			sessions := connected(
				config.MCPServer{Name: "docs", Command: "unused"},
				config.MCPServer{Name: "issues", Command: "unused"},
			)

			Expect(sessions.Names()).To(Equal([]string{"docs", "issues"}))

			for _, name := range sessions.Names() {
				var served string
				var tools []string

				err := sessions.Use(ctx, name, func(session *mcp.ClientSession) error {
					served = session.InitializeResult().ServerInfo.Name

					res, err := session.ListTools(ctx, nil)
					if err != nil {
						return err
					}
					for _, tool := range res.Tools {
						tools = append(tools, tool.Name)
					}

					return nil
				})
				Expect(err).ToNot(HaveOccurred())
				Expect(served).To(Equal(name))
				Expect(tools).To(Equal([]string{"search"}))
			}
		})

		It("should identify this process to the server", func() {
			sessions := connected(config.MCPServer{Name: "docs", Command: "unused"})
			Expect(sessions.Names()).To(HaveLen(1))

			Expect(servers.served()).To(HaveLen(1))
			params := servers.served()[0].InitializeParams()
			Expect(params.ClientInfo.Name).To(Equal("fisk-test"))
			Expect(params.ClientInfo.Version).To(Equal("0.0.1"))
		})

		It("should error on a name that is configured twice", func() {
			_, err := Connect(ctx, Options{
				Servers: []config.MCPServer{{Name: "docs", Command: "unused"}, {Name: "docs", URL: "https://example.net/mcp"}},
				Dialer:  servers.dialer(),
			})
			Expect(err).To(MatchError(ContainSubstring(`mcp server "docs" is configured more than once`)))
		})

		It("should close the sessions it opened when a later server fails", func() {
			servers.fail["issues"] = errors.New("no such host")

			_, err := Connect(ctx, Options{
				Servers: []config.MCPServer{{Name: "docs", Command: "unused"}, {Name: "issues", URL: "https://example.net/mcp"}},
				Dialer:  servers.dialer(),
			})
			Expect(err).To(MatchError(ContainSubstring("no such host")))

			Expect(servers.served()).To(HaveLen(1))
			Eventually(ended(servers.served()[0])).Should(BeClosed())
		})

		It("should resolve a ${VAR} reference and name one that is not set", func() {
			_, err := Connect(ctx, Options{
				Servers: []config.MCPServer{{
					Name:    "docs",
					Command: "does-not-run",
					Env:     map[string]string{"DOCS_TOKEN": "prefix-${FISK_MCPCLIENT_ABSENT}"},
				}},
			})
			Expect(err).To(MatchError(ContainSubstring(`mcp server "docs": env "DOCS_TOKEN": environment variable "FISK_MCPCLIENT_ABSENT" is not set`)))
		})

		It("should resolve a ${VAR} reference in the url and name one that is not set", func() {
			_, err := Connect(ctx, Options{
				Servers: []config.MCPServer{{
					Name: "docs",
					URL:  "https://mcp.example.net/mcp/?apiKey=${FISK_MCPCLIENT_ABSENT}",
				}},
			})
			Expect(err).To(MatchError(ContainSubstring(`mcp server "docs": url: environment variable "FISK_MCPCLIENT_ABSENT" is not set`)))
		})

		It("should fail the connect on a file it cannot read, naming the path and not the content", func() {
			dir := GinkgoT().TempDir()
			readable := filepath.Join(dir, "present")
			Expect(os.WriteFile(readable, []byte(referencedToken), 0600)).To(Succeed())
			absent := filepath.Join(dir, "absent")

			_, err := Connect(ctx, Options{
				Servers: []config.MCPServer{{
					Name:    "docs",
					URL:     "https://mcp.example.net/mcp/?apiKey=${file:" + readable + "}",
					Headers: map[string]string{"Authorization": "Bearer ${file:" + absent + "}"},
				}},
			})
			Expect(err).To(MatchError(os.ErrNotExist))
			Expect(err).To(MatchError(ContainSubstring(`mcp server "docs": headers "Authorization": reading credential file "` + absent + `"`)))
			Expect(err.Error()).ToNot(ContainSubstring(referencedToken))
		})

		// A token a file supplied is searched for the way one a variable supplied is, so
		// a service that takes it in the path does not print it on a refused connection.
		It("should redact a credential a file put in the url path from a failed connect", func() {
			path := filepath.Join(GinkgoT().TempDir(), "path_token")
			Expect(os.WriteFile(path, []byte(referencedToken+"\n"), 0600)).To(Succeed())

			listener, err := net.Listen("tcp", "127.0.0.1:0")
			Expect(err).ToNot(HaveOccurred())

			endpoint := fmt.Sprintf("http://%s/api/mcp/s/${file:%s}/mcp", listener.Addr().String(), path)
			Expect(listener.Close()).To(Succeed())

			_, err = Connect(ctx, Options{
				Servers:  []config.MCPServer{{Name: "docs", URL: endpoint, TimeoutParsed: 10 * time.Second}},
				Identity: "fisk-test",
				Version:  "0.0.1",
			})
			Expect(err).To(HaveOccurred())
			Expect(err.Error()).ToNot(ContainSubstring(referencedToken))
			Expect(err).To(MatchError(ContainSubstring(`connecting to mcp server "docs"`)))
			Expect(err).To(MatchError(ContainSubstring("/api/mcp/s/REDACTED/mcp")))
		})

		// The endpoint of a server that authenticates by query parameter carries the
		// credential itself, and net/http quotes the url it was dialing in the error it
		// returns, so a failed connect is the surface the token would reach a terminal and
		// a log through.
		It("should keep a credential in the url out of a failed connect", func() {
			listener, err := net.Listen("tcp", "127.0.0.1:0")
			Expect(err).ToNot(HaveOccurred())

			// The port is released before anything dials it, so the connect fails on a
			// refused connection rather than on the entry's timeout.
			endpoint := fmt.Sprintf("http://%s/mcp?apiKey=%s", listener.Addr().String(), literalToken)
			Expect(listener.Close()).To(Succeed())

			_, err = Connect(ctx, Options{
				Servers:  []config.MCPServer{{Name: "docs", URL: endpoint, TimeoutParsed: 10 * time.Second}},
				Identity: "fisk-test",
				Version:  "0.0.1",
			})
			Expect(err).To(HaveOccurred())
			Expect(err.Error()).ToNot(ContainSubstring(literalToken))
			Expect(err).To(MatchError(ContainSubstring(`connecting to mcp server "docs"`)))
			Expect(err).To(MatchError(ContainSubstring("apiKey=REDACTED")))
		})

		// Zapier and Composio take the credential in a path segment, where redacting on
		// the shape of a url alone would print it: the endpoint is
		// "https://mcp.zapier.com/api/mcp/s/<token>/mcp" and nothing in it says which
		// segment is the token. What the reference resolved to is searched for instead.
		It("should keep a credential a reference put in the url path out of a failed connect", func() {
			listener, err := net.Listen("tcp", "127.0.0.1:0")
			Expect(err).ToNot(HaveOccurred())

			endpoint := fmt.Sprintf("http://%s/api/mcp/s/${%s}/mcp", listener.Addr().String(), referencedTokenVar)
			Expect(listener.Close()).To(Succeed())

			_, err = Connect(ctx, Options{
				Servers:  []config.MCPServer{{Name: "docs", URL: endpoint, TimeoutParsed: 10 * time.Second}},
				Identity: "fisk-test",
				Version:  "0.0.1",
				LookupEnv: func(name string) (string, bool) {
					if name == referencedTokenVar {
						return referencedToken, true
					}

					return "", false
				},
			})
			Expect(err).To(HaveOccurred())
			Expect(err.Error()).ToNot(ContainSubstring(referencedToken))
			Expect(err).To(MatchError(ContainSubstring(`connecting to mcp server "docs"`)))
			Expect(err).To(MatchError(ContainSubstring("/api/mcp/s/REDACTED/mcp")))
		})

		// A reference in the query string is covered twice over, by the value it
		// resolved to and by the structure of the url, and both were true before the
		// path case was answered.
		It("should keep a credential a reference put in the url query out of a failed connect", func() {
			listener, err := net.Listen("tcp", "127.0.0.1:0")
			Expect(err).ToNot(HaveOccurred())

			endpoint := fmt.Sprintf("http://%s/mcp?apiKey=${%s}", listener.Addr().String(), referencedTokenVar)
			Expect(listener.Close()).To(Succeed())

			_, err = Connect(ctx, Options{
				Servers:  []config.MCPServer{{Name: "docs", URL: endpoint, TimeoutParsed: 10 * time.Second}},
				Identity: "fisk-test",
				Version:  "0.0.1",
				LookupEnv: func(name string) (string, bool) {
					if name == referencedTokenVar {
						return referencedToken, true
					}

					return "", false
				},
			})
			Expect(err).To(HaveOccurred())
			Expect(err.Error()).ToNot(ContainSubstring(referencedToken))
			Expect(err).To(MatchError(ContainSubstring("apiKey=REDACTED")))
		})

		// The residue: an operator who wrote the token into the path as a literal has
		// nothing that identifies it as one, and it reaches the error. It is pinned so
		// the limit stays a decision, and the doc comments on config.RedactURL,
		// MCPServer.SafeURL and MCPServer.URL say the same.
		It("should print a literal credential in the url path of a failed connect", func() {
			listener, err := net.Listen("tcp", "127.0.0.1:0")
			Expect(err).ToNot(HaveOccurred())

			endpoint := fmt.Sprintf("http://%s/api/mcp/s/%s/mcp", listener.Addr().String(), literalToken)
			Expect(listener.Close()).To(Succeed())

			_, err = Connect(ctx, Options{
				Servers:  []config.MCPServer{{Name: "docs", URL: endpoint, TimeoutParsed: 10 * time.Second}},
				Identity: "fisk-test",
				Version:  "0.0.1",
			})
			Expect(err).To(HaveOccurred())
			Expect(err.Error()).To(ContainSubstring(literalToken))
		})

		It("should bound the connect with the entry's timeout", func() {
			deaf := &deafTransport{closed: make(chan struct{})}
			DeferCleanup(deaf.stop)

			started := time.Now()
			_, err := Connect(ctx, Options{
				Servers: []config.MCPServer{{Name: "docs", Command: "unused", TimeoutParsed: 150 * time.Millisecond}},
				Dialer: func(context.Context, config.MCPServer) (mcp.Transport, error) {
					return deaf, nil
				},
			})
			Expect(err).To(MatchError(context.DeadlineExceeded))
			Expect(err).To(MatchError(ContainSubstring(`connecting to mcp server "docs"`)))
			Expect(err).To(MatchError(ContainSubstring("150ms")))
			Expect(time.Since(started)).To(BeNumerically("<", 5*time.Second))
		})

		It("should pass the connect timeout to the dialer", func() {
			var deadline time.Time
			var ok bool

			_, err := Connect(ctx, Options{
				Servers: []config.MCPServer{{Name: "docs", Command: "unused", TimeoutParsed: 250 * time.Millisecond}},
				Dialer: func(dialCtx context.Context, _ config.MCPServer) (mcp.Transport, error) {
					deadline, ok = dialCtx.Deadline()
					return nil, errors.New("not dialed")
				},
			})
			Expect(err).To(MatchError(ContainSubstring("not dialed")))
			Expect(ok).To(BeTrue())
			Expect(deadline).To(BeTemporally("~", time.Now().Add(250*time.Millisecond), 250*time.Millisecond))
		})
	})

	Describe("Use", func() {
		It("should error on a server that is not configured", func() {
			sessions := connected(config.MCPServer{Name: "docs", Command: "unused"})

			called := false
			err := sessions.Use(ctx, "issues", func(*mcp.ClientSession) error {
				called = true
				return nil
			})
			Expect(err).To(MatchError(`no mcp server named "issues" is configured`))
			Expect(called).To(BeFalse())
		})

		It("should return what the caller's function returns", func() {
			sessions := connected(config.MCPServer{Name: "docs", Command: "unused"})

			err := sessions.Use(ctx, "docs", func(*mcp.ClientSession) error {
				return errors.New("the tool call failed")
			})
			Expect(err).To(MatchError("the tool call failed"))
		})

		It("should replace a session that has ended", func() {
			sessions := connected(config.MCPServer{Name: "docs", Command: "unused"})

			Expect(servers.served()).To(HaveLen(1))
			servers.breakLink("docs")

			// The client learns the session ended asynchronously, so wait for the
			// replacement rather than for the first call to fail.
			Eventually(func() int {
				_ = sessions.Use(ctx, "docs", func(*mcp.ClientSession) error { return nil })

				servers.mu.Lock()
				defer servers.mu.Unlock()

				return servers.dials["docs"]
			}).Should(Equal(2))

			var tools int
			err := sessions.Use(ctx, "docs", func(session *mcp.ClientSession) error {
				res, err := session.ListTools(ctx, nil)
				if err != nil {
					return err
				}
				tools = len(res.Tools)

				return nil
			})
			Expect(err).ToNot(HaveOccurred())
			Expect(tools).To(Equal(1))
		})

		It("should close the session it replaces", func() {
			// watch_tools is what opens the subscriptions/listen stream this counts the
			// watchers of.
			sessions := connected(config.MCPServer{Name: "docs", Command: "unused", WatchTools: true})

			// The watcher of the live session's subscriptions/listen stream. Asserting it
			// is running before anything is broken is what makes the count below evidence
			// rather than a probe that matches nothing.
			Eventually(listenGoroutines).Should(Equal(1))

			for i := 0; i < 5; i++ {
				servers.breakLink("docs")

				// The client learns a session ended asynchronously, so the call is repeated
				// until it reaches the replacement rather than racing it.
				Eventually(func() error {
					return sessions.Use(ctx, "docs", func(session *mcp.ClientSession) error {
						_, err := session.ListTools(ctx, nil)
						return err
					})
				}).Should(Succeed())
			}

			servers.mu.Lock()
			dials := servers.dials["docs"]
			servers.mu.Unlock()
			Expect(dials).To(Equal(6))

			// Every replaced session was closed, so the only watcher left is the live
			// session's. Closing runs on its own goroutine, which is what Eventually waits
			// for.
			Eventually(listenGoroutines).Should(Equal(1))
		})

		It("should be safe for concurrent use", func() {
			sessions := connected(
				config.MCPServer{Name: "docs", Command: "unused"},
				config.MCPServer{Name: "issues", Command: "unused"},
			)

			var wg sync.WaitGroup
			errs := make(chan error, 40)

			for i := 0; i < 20; i++ {
				for _, name := range sessions.Names() {
					wg.Add(1)
					go func(name string) {
						defer wg.Done()
						defer GinkgoRecover()

						errs <- sessions.Use(ctx, name, func(session *mcp.ClientSession) error {
							_, err := session.CallTool(ctx, &mcp.CallToolParams{Name: "search"})
							return err
						})
					}(name)
				}
			}

			wg.Wait()
			close(errs)

			for err := range errs {
				Expect(err).ToNot(HaveOccurred())
			}
		})

		It("should refuse rather than hand fn a nil session when Close lands mid-call", func() {
			// live reads the closed flag under the Sessions lock and the session under
			// the entry lock. A Close arriving between the two clears the session before
			// the channel that reports the session ended is closed, so a caller was
			// handed a nil session. Each round releases the callers and one Close from
			// the same barrier, and each caller calls until it is told the sessions are
			// closed, so Close reaches the entry lock with callers queued behind it.
			const (
				rounds  = 50
				callers = 8
				calls   = 500
			)

			handedNil := errors.New("fn was handed a nil session")

			for round := 0; round < rounds; round++ {
				sessions, err := Connect(ctx, Options{
					Servers:  []config.MCPServer{{Name: "docs", Command: "unused"}},
					Identity: "fisk-test",
					Dialer:   servers.dialer(),
				})
				Expect(err).ToNot(HaveOccurred())

				start := make(chan struct{})
				errs := make(chan error, callers)

				var wg sync.WaitGroup

				for i := 0; i < callers; i++ {
					wg.Add(1)
					go func() {
						defer wg.Done()
						defer GinkgoRecover()

						<-start

						// The call cap only stops the loop if Close never lands, which would
						// leave the round proving nothing rather than hanging.
						for call := 0; call < calls; call++ {
							err := sessions.Use(ctx, "docs", func(session *mcp.ClientSession) error {
								if session == nil {
									return handedNil
								}

								return nil
							})
							if err != nil {
								errs <- err
								return
							}
						}
					}()
				}

				wg.Add(1)
				go func() {
					defer wg.Done()
					defer GinkgoRecover()

					<-start

					Expect(sessions.Close(ctx)).To(Succeed())
				}()

				close(start)
				wg.Wait()
				close(errs)

				// A caller that raced Close gets the same error it would get after Close
				// had finished, and never the sentinel a nil session returns.
				for err := range errs {
					Expect(err).To(MatchError(ContainSubstring("are closed")))
				}
			}
		})
	})

	Describe("CheckServers", func() {
		It("should accept the same set in any order", func() {
			sessions := connected(
				config.MCPServer{Name: "docs", Command: "unused"},
				config.MCPServer{Name: "issues", Command: "unused"},
			)

			Expect(sessions.CheckServers([]config.MCPServer{{Name: "issues"}, {Name: "docs"}})).To(Succeed())
		})

		// Only the names are compared: an alias and a filter belong to whoever opened
		// the session, and a borrower has no standing to overrule them.
		It("should ignore an alias and a filter that differ", func() {
			sessions := connected(config.MCPServer{Name: "docs", Command: "unused"})

			err := sessions.CheckServers([]config.MCPServer{{
				Name:    "docs",
				Alias:   "d",
				Include: &config.ToolFilter{Tools: []string{"^search$"}},
			}})
			Expect(err).ToNot(HaveOccurred())
		})

		It("should name a server that is configured and not connected", func() {
			sessions := connected(config.MCPServer{Name: "docs", Command: "unused"})

			err := sessions.CheckServers([]config.MCPServer{{Name: "docs"}, {Name: "wiki"}})
			Expect(err).To(MatchError(ContainSubstring("configured but not connected: wiki")))
		})

		It("should name a server that is connected and not configured", func() {
			sessions := connected(
				config.MCPServer{Name: "docs", Command: "unused"},
				config.MCPServer{Name: "issues", Command: "unused"},
			)

			err := sessions.CheckServers([]config.MCPServer{{Name: "docs"}})
			Expect(err).To(MatchError(ContainSubstring("connected but not configured: issues")))
		})

		It("should name both directions when the sets differ both ways", func() {
			sessions := connected(config.MCPServer{Name: "docs", Command: "unused"})

			err := sessions.CheckServers([]config.MCPServer{{Name: "wiki"}})
			Expect(err).To(MatchError(ContainSubstring("configured but not connected: wiki")))
			Expect(err).To(MatchError(ContainSubstring("connected but not configured: docs")))
		})
	})

	Describe("Close", func() {
		It("should close every session", func() {
			sessions, err := Connect(ctx, Options{
				Servers: []config.MCPServer{{Name: "docs", Command: "unused"}, {Name: "issues", Command: "unused"}},
				Dialer:  servers.dialer(),
			})
			Expect(err).ToNot(HaveOccurred())

			served := servers.served()
			Expect(served).To(HaveLen(2))

			Expect(sessions.Close(ctx)).To(Succeed())

			for _, session := range served {
				Eventually(ended(session)).Should(BeClosed())
			}
		})

		It("should be idempotent and refuse use afterwards", func() {
			sessions, err := Connect(ctx, Options{
				Servers: []config.MCPServer{{Name: "docs", Command: "unused"}},
				Dialer:  servers.dialer(),
			})
			Expect(err).ToNot(HaveOccurred())

			Expect(sessions.Close(ctx)).To(Succeed())
			Expect(sessions.Close(ctx)).To(Succeed())

			err = sessions.Use(ctx, "docs", func(*mcp.ClientSession) error { return nil })
			Expect(err).To(MatchError(ContainSubstring("are closed")))
		})
	})

	Describe("Capabilities", func() {
		// The wire, not the SDK types, is what these read: mcp.ClientCapabilities
		// marshals on its own to {"roots":{}}, because its deprecated Roots field is a
		// non-pointer struct that encoding/json will not omit, and that is not what a
		// server receives. Both handshakes are covered because the SDK picks between
		// them by what the server answers.
		It("should advertise nothing on the stateless discover handshake", func() {
			clientSide, serverSide := mcp.NewInMemoryTransports()

			srv := mcp.NewServer(&mcp.Implementation{Name: "docs", Version: "1"}, nil)
			_, err := srv.Connect(context.Background(), serverSide, nil)
			Expect(err).ToNot(HaveOccurred())

			recorder := &recordingTransport{inner: clientSide}
			sessions, err := Connect(ctx, Options{
				Servers:  []config.MCPServer{{Name: "docs", Command: "unused"}},
				Identity: "fisk-test",
				Dialer: func(context.Context, config.MCPServer) (mcp.Transport, error) {
					return recorder, nil
				},
			})
			Expect(err).ToNot(HaveOccurred())
			DeferCleanup(func() { Expect(sessions.Close(ctx)).To(Succeed()) })

			written := recorder.written()
			Expect(written).ToNot(BeEmpty())
			Expect(written[0]).To(ContainSubstring(`"method":"server/discover"`))
			Expect(written[0]).To(ContainSubstring(`"io.modelcontextprotocol/clientCapabilities":{}`))
			Expect(written[0]).ToNot(ContainSubstring("roots"))
		})

		It("should advertise nothing on the initialize handshake", func() {
			// A server that refuses server/discover, so the client falls back to the
			// initialize handshake, hand written because the SDK's own server answers
			// discover and would never take this path.
			clientReads, serverWrites := io.Pipe()
			serverReads, clientWrites := io.Pipe()

			initialize := make(chan string, 1)

			go func() {
				defer GinkgoRecover()

				lines := bufio.NewScanner(serverReads)
				for lines.Scan() {
					var msg struct {
						ID     json.RawMessage `json:"id"`
						Method string          `json:"method"`
					}
					if json.Unmarshal(lines.Bytes(), &msg) != nil {
						continue
					}

					switch msg.Method {
					case "server/discover":
						fmt.Fprintf(serverWrites, `{"jsonrpc":"2.0","id":%s,"error":{"code":-32601,"message":"no discover here"}}`+"\n", msg.ID)
					case "initialize":
						initialize <- lines.Text()
						fmt.Fprintf(serverWrites, `{"jsonrpc":"2.0","id":%s,"result":{"protocolVersion":"2025-06-18","capabilities":{},"serverInfo":{"name":"docs","version":"1"}}}`+"\n", msg.ID)
					}
				}
			}()

			sessions, err := Connect(ctx, Options{
				Servers:  []config.MCPServer{{Name: "docs", Command: "unused"}},
				Identity: "fisk-test",
				Dialer: func(context.Context, config.MCPServer) (mcp.Transport, error) {
					return &mcp.IOTransport{Reader: clientReads, Writer: clientWrites}, nil
				},
			})
			Expect(err).ToNot(HaveOccurred())
			DeferCleanup(func() { Expect(sessions.Close(ctx)).To(Succeed()) })

			var params string
			Eventually(initialize).Should(Receive(&params))
			Expect(params).To(ContainSubstring(`"capabilities":{}`))
			Expect(params).ToNot(ContainSubstring("roots"))
			Expect(params).To(ContainSubstring(`"name":"fisk-test"`))
		})
	})

	Describe("Tool list subscription", func() {
		// What Connect subscribes to is settled by whether a tool-list handler is set,
		// and only the bytes say whether the stream was opened.
		subscribed := func(server config.MCPServer) func() []string {
			GinkgoHelper()

			clientSide, serverSide := mcp.NewInMemoryTransports()

			srv := mcp.NewServer(&mcp.Implementation{Name: "docs", Version: "1"}, &mcp.ServerOptions{
				Capabilities: &mcp.ServerCapabilities{Tools: &mcp.ToolCapabilities{ListChanged: true}},
			})
			_, err := srv.Connect(context.Background(), serverSide, nil)
			Expect(err).ToNot(HaveOccurred())

			recorder := &recordingTransport{inner: clientSide}
			sessions, err := Connect(ctx, Options{
				Servers:  []config.MCPServer{server},
				Identity: "fisk-test",
				Dialer: func(context.Context, config.MCPServer) (mcp.Transport, error) {
					return recorder, nil
				},
			})
			Expect(err).ToNot(HaveOccurred())
			DeferCleanup(func() { Expect(sessions.Close(ctx)).To(Succeed()) })

			return recorder.written
		}

		It("should open no listen stream with watch_tools unset", func() {
			written := subscribed(config.MCPServer{Name: "docs", Command: "unused"})

			Expect(written()).ToNot(BeEmpty())
			Consistently(written, 300*time.Millisecond).ShouldNot(ContainElement(ContainSubstring("subscriptions/listen")))
		})

		It("should open a listen stream with watch_tools set", func() {
			written := subscribed(config.MCPServer{Name: "docs", Command: "unused", WatchTools: true})

			Eventually(written).Should(ContainElement(ContainSubstring(`"method":"subscriptions/listen"`)))
		})
	})
})

// deafTransport is a transport whose connection accepts everything written to it
// and never answers, so a connect runs out of time rather than failing.
type deafTransport struct {
	closed chan struct{}
	once   sync.Once
}

func (t *deafTransport) Connect(context.Context) (mcp.Connection, error) {
	return t, nil
}

func (t *deafTransport) Read(ctx context.Context) (jsonrpc.Message, error) {
	select {
	case <-t.closed:
		return nil, io.EOF
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

func (t *deafTransport) Write(context.Context, jsonrpc.Message) error { return nil }
func (t *deafTransport) SessionID() string                            { return "" }

func (t *deafTransport) Close() error {
	t.stop()
	return nil
}

func (t *deafTransport) stop() {
	t.once.Do(func() { close(t.closed) })
}
