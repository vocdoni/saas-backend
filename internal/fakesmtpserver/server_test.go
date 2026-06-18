package fakesmtpserver

import (
	"bufio"
	"context"
	"net"
	"strconv"
	"strings"
	"testing"
	"time"
)

func getFreePort() (string, error) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return "", err
	}
	defer func() {
		_ = listener.Close()
	}()
	return listener.Addr().String(), nil
}

func splitHostPort(address string) (string, int, error) {
	host, portStr, err := net.SplitHostPort(address)
	if err != nil {
		return "", 0, err
	}
	port, err := strconv.Atoi(portStr)
	if err != nil {
		return "", 0, err
	}
	return host, port, nil
}

func TestFakeSMTPServer(t *testing.T) {
	inbox := make(chan string, 1)
	address, err := getFreePort()
	if err != nil {
		t.Fatalf("Failed to get free port: %v", err)
	}
	host, port, err := splitHostPort(address)
	if err != nil {
		t.Fatalf("Failed to split host and port: %v", err)
	}
	server := NewServer(host, port, inbox)
	ctx, cancel := context.WithCancel(t.Context()) // Fixed incorrect t.Context()
	defer cancel()

	errChan := make(chan error, 1) // Channel to capture errors from the goroutine

	// Start the server
	go func() {
		if err := server.Start(ctx); err != nil {
			errChan <- err // Send error to the channel
		}
		close(errChan) // Close the channel when done
	}()
	time.Sleep(100 * time.Millisecond) // Give the server time to start

	// Check for errors from the goroutine
	select {
	case err := <-errChan:
		if err != nil {
			t.Fatalf("Failed to start server: %v", err)
		}
	default:
		// No error, continue with the test
	}

	// Connect to the server and send an email
	conn, err := net.Dial("tcp", address)
	if err != nil {
		t.Fatalf("Failed to connect to server: %v", err)
	}
	defer func() {
		_ = conn.Close()
	}()

	reader := bufio.NewReader(conn)
	writer := bufio.NewWriter(conn)

	// Read greeting
	if greeting, _ := reader.ReadString('\n'); !strings.HasPrefix(greeting, "220") {
		t.Fatalf("Expected greeting, got: %s", greeting)
	}

	// Send HELO
	if _, err := writer.WriteString("HELO localhost\r\n"); err != nil {
		t.Fatalf("Failed to write HELO command: %v", err)
	}
	if err := writer.Flush(); err != nil {
		t.Fatalf("Failed to flush writer: %v", err)
	}
	if response, _ := reader.ReadString('\n'); !strings.HasPrefix(response, "250") {
		t.Fatalf("Expected HELO response, got: %s", response)
	}

	// Send MAIL FROM
	if _, err := writer.WriteString("MAIL FROM:<test@example.com>\r\n"); err != nil {
		t.Fatalf("Failed to write MAIL FROM command: %v", err)
	}
	if err := writer.Flush(); err != nil {
		t.Fatalf("Failed to flush writer: %v", err)
	}
	if response, _ := reader.ReadString('\n'); !strings.HasPrefix(response, "250") {
		t.Fatalf("Expected MAIL FROM response, got: %s", response)
	}

	// Send RCPT TO
	if _, err := writer.WriteString("RCPT TO:<recipient@example.com>\r\n"); err != nil {
		t.Fatalf("Failed to write RCPT TO command: %v", err)
	}
	if err := writer.Flush(); err != nil {
		t.Fatalf("Failed to flush writer: %v", err)
	}
	if response, _ := reader.ReadString('\n'); !strings.HasPrefix(response, "250") {
		t.Fatalf("Expected RCPT TO response, got: %s", response)
	}

	// Send DATA
	if _, err := writer.WriteString("DATA\r\n"); err != nil {
		t.Fatalf("Failed to write DATA command: %v", err)
	}
	if err := writer.Flush(); err != nil {
		t.Fatalf("Failed to flush writer: %v", err)
	}
	if response, _ := reader.ReadString('\n'); !strings.HasPrefix(response, "354") {
		t.Fatalf("Expected DATA response, got: %s", response)
	}

	// Send email content
	if _, err := writer.WriteString("Subject: Test Email\r\n\r\nThis is a test email.\r\n.\r\n"); err != nil {
		t.Fatalf("Failed to write email content: %v", err)
	}
	if err := writer.Flush(); err != nil {
		t.Fatalf("Failed to flush writer: %v", err)
	}
	if response, _ := reader.ReadString('\n'); !strings.HasPrefix(response, "250") {
		t.Fatalf("Expected email content response, got: %s", response)
	}

	// Send QUIT
	if _, err := writer.WriteString("QUIT\r\n"); err != nil {
		t.Fatalf("Failed to write QUIT command: %v", err)
	}
	if err := writer.Flush(); err != nil {
		t.Fatalf("Failed to flush writer: %v", err)
	}
	if response, _ := reader.ReadString('\n'); !strings.HasPrefix(response, "221") {
		t.Fatalf("Expected QUIT response, got: %s", response)
	}

	// Verify email content in inbox
	select {
	case email := <-inbox:
		if !strings.Contains(email, "Subject: Test Email") || !strings.Contains(email, "This is a test email.") {
			t.Fatalf("Unexpected email content: %s", email)
		}
	case <-time.After(1 * time.Second):
		t.Fatal("Timeout waiting for email in inbox")
	}

	// Stop the server
	server.Stop()
}

func TestFakeSMTPServer_UnsupportedCommand(t *testing.T) {
	inbox := make(chan string, 1)
	address, err := getFreePort()
	if err != nil {
		t.Fatalf("Failed to get free port: %v", err)
	}
	host, port, err := splitHostPort(address)
	if err != nil {
		t.Fatalf("Failed to split host and port: %v", err)
	}
	server := NewServer(host, port, inbox)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	errChan := make(chan error, 1)

	// Start the server
	go func() {
		if err := server.Start(ctx); err != nil {
			errChan <- err
		}
		close(errChan)
	}()
	time.Sleep(100 * time.Millisecond)

	// Check for errors from the goroutine
	select {
	case err := <-errChan:
		if err != nil {
			t.Fatalf("Failed to start server: %v", err)
		}
	default:
	}

	// Connect to the server and send an unsupported command
	conn, err := net.Dial("tcp", address)
	if err != nil {
		t.Fatalf("Failed to connect to server: %v", err)
	}
	defer func() {
		_ = conn.Close()
	}()

	reader := bufio.NewReader(conn)
	writer := bufio.NewWriter(conn)

	// Read greeting
	if greeting, _ := reader.ReadString('\n'); !strings.HasPrefix(greeting, "220") {
		t.Fatalf("Expected greeting, got: %s", greeting)
	}

	// Send unsupported command
	if _, err := writer.WriteString("FOO BAR\r\n"); err != nil {
		t.Fatalf("Failed to write unsupported command: %v", err)
	}
	if err := writer.Flush(); err != nil {
		t.Fatalf("Failed to flush writer: %v", err)
	}
	if response, _ := reader.ReadString('\n'); !strings.HasPrefix(response, "250") {
		t.Fatalf("Expected default response, got: %s", response)
	}

	// Stop the server
	server.Stop()
}

func TestFakeSMTPServer_BadAddress(t *testing.T) {
	inbox := make(chan string, 1)
	server := NewServer("invalid-address", 2527, inbox) // Added a dummy port
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()

	if err := server.Start(ctx); err == nil {
		t.Fatalf("Expected error starting server with bad address, got nil")
	}
}
