// Package fakesmtpserver provides a simple SMTP server for testing purposes.
// It allows you to simulate an SMTP server that can receive emails and publish
// them to an inbox channel. If the inbox channel is full, messages are dropped
// to avoid blocking SMTP sessions. The server can be started and stopped, and
// it handles basic SMTP commands like HELO, MAIL FROM, RCPT TO, and DATA. It
// also provides a way to retrieve the received emails from the inbox channel.
package fakesmtpserver

import (
	"bufio"
	"context"
	"fmt"
	"net"
	"strings"
	"sync"
)

// FakeSMTPServer represents a simple SMTP testing server.
type FakeSMTPServer struct {
	addr     string
	inbox    chan string
	listener net.Listener
	mu       sync.Mutex // Mutex to protect listener
}

// NewServer creates a new FakeSMTPServer instance that listens on the given
// address and port and stores the received emails in the inbox channel
// provided.
func NewServer(addr string, port int, inbox chan string) *FakeSMTPServer {
	return &FakeSMTPServer{
		addr:  fmt.Sprintf("%s:%d", addr, port),
		inbox: inbox,
	}
}

// Start method launches the test SMTP server.
func (s *FakeSMTPServer) Start(ctx context.Context) error {
	var err error
	s.mu.Lock()
	s.listener, err = net.Listen("tcp", s.addr)
	s.mu.Unlock()
	if err != nil {
		return err
	}

	listener := s.listener
	// use Stop function to safely close the listener when the context is done
	go func() {
		<-ctx.Done()
		s.Stop()
	}()
	// start accepting connections in a separate goroutine
	go func() {
		for {
			conn, err := listener.Accept()
			if err != nil {
				return
			}
			go s.handleConn(conn)
		}
	}()
	return nil
}

// Stop method shuts down the test SMTP server.
func (s *FakeSMTPServer) Stop() {
	// copy listener under lock
	s.mu.Lock()
	listener := s.listener
	// set listener to nil under lock and unlock
	s.listener = nil
	s.mu.Unlock()
	// close the listener if it is not nil
	if listener != nil {
		_ = listener.Close()
	}
}

func (s *FakeSMTPServer) handleConn(conn net.Conn) {
	defer func() {
		_ = conn.Close()
	}()
	reader := bufio.NewReader(conn)
	// send greeting
	if _, err := fmt.Fprintf(conn, "220 Fake SMTP Service Ready\r\n"); err != nil {
		return
	}
	var dataBuilder strings.Builder
	inData := false
	// read incoming data
	for {
		line, err := reader.ReadString('\n')
		if err != nil {
			return
		}
		line = strings.TrimRight(line, "\r\n")
		// check if we are in the data section
		if inData {
			if line == "." {
				inData = false
				// send back a confirmation and store the data
				if _, err := fmt.Fprintf(conn, "250 OK\r\n"); err != nil {
					return
				}
				select {
				case s.inbox <- dataBuilder.String():
				default:
					// Drop message if inbox is full
				}
				dataBuilder.Reset()
				continue
			}
			_, _ = dataBuilder.WriteString(line + "\n")
			continue
		}
		// simple command handling
		switch {
		case strings.HasPrefix(line, "HELO"), strings.HasPrefix(line, "EHLO"):
			_, _ = fmt.Fprintf(conn, "250 Hello\r\n")
		case strings.HasPrefix(line, "MAIL FROM:"):
			_, _ = fmt.Fprintf(conn, "250 OK\r\n")
		case strings.HasPrefix(line, "RCPT TO:"):
			_, _ = fmt.Fprintf(conn, "250 OK\r\n")
		case strings.HasPrefix(line, "DATA"):
			// prepare to receive data
			_, _ = fmt.Fprintf(conn, "354 End data with <CR><LF>.<CR><LF>\r\n")
			inData = true
		case strings.HasPrefix(line, "QUIT"):
			// close the connection
			_, _ = fmt.Fprintf(conn, "221 Bye\r\n")
			return
		default:
			_, _ = fmt.Fprintf(conn, "250 OK\r\n")
		}
	}
}
