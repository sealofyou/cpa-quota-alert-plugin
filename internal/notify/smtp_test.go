package notify

import (
	"bufio"
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"math/big"
	"net"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/sealofyou/cpa-quota-alert-plugin/internal/config"
)

func TestSMTPSenderImplicitTLSAndSTARTTLS(t *testing.T) {
	cert, pool := testCertificate(t)
	for _, mode := range []string{"implicit_tls", "starttls"} {
		t.Run(mode, func(t *testing.T) {
			server := startFakeSMTP(t, mode == "implicit_tls", cert)
			defer server.close()

			_, portText, err := net.SplitHostPort(server.addr)
			if err != nil {
				t.Fatalf("split addr: %v", err)
			}
			port, err := strconv.Atoi(portText)
			if err != nil {
				t.Fatalf("parse port: %v", err)
			}
			sender := NewSMTPSender(config.SMTPConfig{
				Enabled:        true,
				Host:           "localhost",
				Port:           port,
				TLSMode:        mode,
				TimeoutSeconds: 5,
				UsernameEnv:    "SMTP_USERNAME",
				PasswordEnv:    "SMTP_PASSWORD",
				RecipientsEnv:  "SMTP_RECIPIENTS",
				FromEnv:        "SMTP_FROM",
				FromName:       "CPA Quota Alert",
			}, func(key string) string {
				switch key {
				case "SMTP_USERNAME":
					return "smtp-user"
				case "SMTP_PASSWORD":
					return "smtp-password"
				case "SMTP_RECIPIENTS":
					return "alerts@example.com, ops@example.com"
				case "SMTP_FROM":
					return "quota@example.com"
				default:
					return ""
				}
			})
			sender.tlsConfig = &tls.Config{RootCAs: pool, ServerName: "localhost", MinVersion: tls.VersionTLS12}

			err = sender.Send(context.Background(), Message{
				EventType:  "quota.low",
				Subject:    "Quota low",
				Body:       "Quota remaining is low.",
				OccurredAt: time.Date(2026, 8, 6, 1, 2, 3, 0, time.UTC),
			})
			if err != nil {
				t.Fatalf("Send: %v", err)
			}
			data := server.data(t)
			if !strings.Contains(data, "From: \"CPA Quota Alert\" <quota@example.com>") || !strings.Contains(data, "To: <alerts@example.com>, <ops@example.com>") || !strings.Contains(data, "Quota remaining is low.") {
				t.Fatalf("unexpected message data: %q", data)
			}
		})
	}
}

func TestSMTPSenderRejectsHeaderInjectionAndRedactsErrors(t *testing.T) {
	sender := NewSMTPSender(config.SMTPConfig{
		Enabled:        true,
		Host:           "localhost",
		Port:           1,
		TLSMode:        "starttls",
		TimeoutSeconds: 1,
		UsernameEnv:    "SMTP_USERNAME",
		PasswordEnv:    "SMTP_PASSWORD",
		RecipientsEnv:  "SMTP_RECIPIENTS",
		FromEnv:        "SMTP_FROM",
	}, func(key string) string {
		if key == "SMTP_PASSWORD" {
			return "test-password-value"
		}
		return "configured"
	})
	err := sender.Send(context.Background(), Message{EventType: "quota.low", Subject: "Quota low\r\nBcc: leak@example.com", Body: "body"})
	if !isStableError(err, "notify_header_injection") || strings.Contains(err.Error(), "test-password-value") || strings.Contains(err.Error(), "leak@example.com") {
		t.Fatalf("expected stable redacted header injection error, got %v", err)
	}
}

func TestSMTPSenderCancelsStalledProtocolBeforeConfigTimeout(t *testing.T) {
	cert, pool := testCertificate(t)
	server := startStalledAuthSMTP(t, cert)
	defer server.close()

	_, portText, err := net.SplitHostPort(server.addr)
	if err != nil {
		t.Fatalf("split addr: %v", err)
	}
	port, err := strconv.Atoi(portText)
	if err != nil {
		t.Fatalf("parse port: %v", err)
	}
	sender := NewSMTPSender(config.SMTPConfig{
		Enabled:        true,
		Host:           "localhost",
		Port:           port,
		TLSMode:        "starttls",
		TimeoutSeconds: 30,
		UsernameEnv:    "SMTP_USERNAME",
		PasswordEnv:    "SMTP_PASSWORD",
		RecipientsEnv:  "SMTP_RECIPIENTS",
		FromEnv:        "SMTP_FROM",
	}, validSMTPEnv)
	sender.tlsConfig = &tls.Config{RootCAs: pool, ServerName: "localhost", MinVersion: tls.VersionTLS12}

	ctx, cancel := context.WithCancel(context.Background())
	errCh := make(chan error, 1)
	started := time.Now()
	go func() {
		errCh <- sender.Send(ctx, Message{EventType: "quota.low", Subject: "Quota low", Body: "body"})
	}()

	select {
	case <-server.authSeen:
	case err := <-errCh:
		t.Fatalf("Send returned before stalled AUTH was reached: %v", err)
	case <-time.After(2 * time.Second):
		t.Fatalf("timeout waiting for stalled AUTH")
	}
	cancel()

	select {
	case err := <-errCh:
		if !isStableError(err, "smtp_auth_failed") || strings.Contains(err.Error(), "stalled provider text") {
			t.Fatalf("expected stable redacted auth failure, got %v", err)
		}
		if time.Since(started) > 2*time.Second {
			t.Fatalf("Send did not return promptly after context cancellation")
		}
	case <-time.After(2 * time.Second):
		t.Fatalf("Send did not return after context cancellation")
	}
}

func TestSMTPSenderSTARTTLSUnavailableIsStableRedactedError(t *testing.T) {
	server := startNoSTARTTLSSMTP(t)
	defer server.close()

	_, portText, err := net.SplitHostPort(server.addr)
	if err != nil {
		t.Fatalf("split addr: %v", err)
	}
	port, err := strconv.Atoi(portText)
	if err != nil {
		t.Fatalf("parse port: %v", err)
	}
	sender := NewSMTPSender(config.SMTPConfig{
		Enabled:        true,
		Host:           "localhost",
		Port:           port,
		TLSMode:        "starttls",
		TimeoutSeconds: 5,
		UsernameEnv:    "SMTP_USERNAME",
		PasswordEnv:    "SMTP_PASSWORD",
		RecipientsEnv:  "SMTP_RECIPIENTS",
		FromEnv:        "SMTP_FROM",
	}, validSMTPEnv)

	err = sender.Send(context.Background(), Message{EventType: "quota.low", Subject: "Quota low", Body: "body"})
	if !isStableError(err, "smtp_tls_failed") || strings.Contains(err.Error(), "provider") {
		t.Fatalf("expected stable redacted STARTTLS failure, got %v", err)
	}
}

func validSMTPEnv(key string) string {
	switch key {
	case "SMTP_USERNAME":
		return "smtp-user"
	case "SMTP_PASSWORD":
		return "smtp-password"
	case "SMTP_RECIPIENTS":
		return "alerts@example.com"
	case "SMTP_FROM":
		return "quota@example.com"
	default:
		return ""
	}
}

type fakeSMTPServer struct {
	addr     string
	listener net.Listener
	resultCh chan smtpResult
}

type smtpResult struct {
	data string
	err  error
}

func startFakeSMTP(t *testing.T, implicitTLS bool, cert tls.Certificate) *fakeSMTPServer {
	t.Helper()
	ln, err := net.Listen("tcp", "localhost:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	server := &fakeSMTPServer{addr: ln.Addr().String(), listener: ln, resultCh: make(chan smtpResult, 1)}
	go func() {
		conn, err := ln.Accept()
		if err != nil {
			server.resultCh <- smtpResult{err: err}
			return
		}
		if implicitTLS {
			conn = tls.Server(conn, &tls.Config{Certificates: []tls.Certificate{cert}, MinVersion: tls.VersionTLS12})
		}
		data, err := handleFakeSMTP(conn, cert)
		server.resultCh <- smtpResult{data: data, err: err}
	}()
	return server
}

type stalledAuthSMTPServer struct {
	addr     string
	listener net.Listener
	authSeen chan struct{}
}

func startStalledAuthSMTP(t *testing.T, cert tls.Certificate) *stalledAuthSMTPServer {
	t.Helper()
	ln, err := net.Listen("tcp", "localhost:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	server := &stalledAuthSMTPServer{addr: ln.Addr().String(), listener: ln, authSeen: make(chan struct{})}
	go func() {
		conn, err := ln.Accept()
		if err != nil {
			return
		}
		defer conn.Close()
		reader := bufio.NewReader(conn)
		writer := bufio.NewWriter(conn)
		writeSMTP(writer, "220 localhost ESMTP")
		for {
			line, err := reader.ReadString('\n')
			if err != nil {
				return
			}
			cmd := strings.ToUpper(strings.TrimSpace(line))
			switch {
			case strings.HasPrefix(cmd, "EHLO") || strings.HasPrefix(cmd, "HELO"):
				writeRawSMTP(writer, "250-localhost\r\n250-STARTTLS\r\n250 AUTH PLAIN\r\n")
			case strings.HasPrefix(cmd, "STARTTLS"):
				writeSMTP(writer, "220 ready")
				tlsConn := tls.Server(conn, &tls.Config{Certificates: []tls.Certificate{cert}, MinVersion: tls.VersionTLS12})
				if err := tlsConn.Handshake(); err != nil {
					return
				}
				conn = tlsConn
				reader = bufio.NewReader(conn)
				writer = bufio.NewWriter(conn)
			case strings.HasPrefix(cmd, "AUTH"):
				close(server.authSeen)
				_, _ = reader.ReadString('\n')
				return
			default:
				writeSMTP(writer, "250 ok")
			}
		}
	}()
	return server
}

func (s *stalledAuthSMTPServer) close() { _ = s.listener.Close() }

type noSTARTTLSSMTPServer struct {
	addr     string
	listener net.Listener
	done     chan struct{}
}

func startNoSTARTTLSSMTP(t *testing.T) *noSTARTTLSSMTPServer {
	t.Helper()
	ln, err := net.Listen("tcp", "localhost:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	server := &noSTARTTLSSMTPServer{addr: ln.Addr().String(), listener: ln, done: make(chan struct{})}
	go func() {
		defer close(server.done)
		conn, err := ln.Accept()
		if err != nil {
			return
		}
		defer conn.Close()
		reader := bufio.NewReader(conn)
		writer := bufio.NewWriter(conn)
		writeSMTP(writer, "220 localhost ESMTP")
		for {
			line, err := reader.ReadString('\n')
			if err != nil {
				return
			}
			cmd := strings.ToUpper(strings.TrimSpace(line))
			if strings.HasPrefix(cmd, "EHLO") || strings.HasPrefix(cmd, "HELO") {
				writeRawSMTP(writer, "250-localhost\r\n250 AUTH PLAIN\r\n")
				return
			}
			writeSMTP(writer, "250 ok")
		}
	}()
	return server
}

func (s *noSTARTTLSSMTPServer) close() {
	_ = s.listener.Close()
	<-s.done
}

func (s *fakeSMTPServer) close() { _ = s.listener.Close() }

func (s *fakeSMTPServer) data(t *testing.T) string {
	t.Helper()
	select {
	case result := <-s.resultCh:
		if result.err != nil && !strings.Contains(result.err.Error(), "use of closed network connection") {
			t.Fatalf("server error: %v", result.err)
		}
		return result.data
	case <-time.After(5 * time.Second):
		t.Fatalf("timeout waiting for SMTP result")
		return ""
	}
}

func handleFakeSMTP(conn net.Conn, cert tls.Certificate) (string, error) {
	defer conn.Close()
	reader := bufio.NewReader(conn)
	writer := bufio.NewWriter(conn)
	writeSMTP(writer, "220 localhost ESMTP")
	var data strings.Builder
	for {
		line, err := reader.ReadString('\n')
		if err != nil {
			return "", err
		}
		cmd := strings.ToUpper(strings.TrimSpace(line))
		switch {
		case strings.HasPrefix(cmd, "EHLO") || strings.HasPrefix(cmd, "HELO"):
			writeRawSMTP(writer, "250-localhost\r\n250-STARTTLS\r\n250 AUTH PLAIN\r\n")
		case strings.HasPrefix(cmd, "STARTTLS"):
			writeSMTP(writer, "220 ready")
			tlsConn := tls.Server(conn, &tls.Config{Certificates: []tls.Certificate{cert}, MinVersion: tls.VersionTLS12})
			if err := tlsConn.Handshake(); err != nil {
				return "", err
			}
			conn = tlsConn
			reader = bufio.NewReader(conn)
			writer = bufio.NewWriter(conn)
		case strings.HasPrefix(cmd, "AUTH"):
			writeSMTP(writer, "235 ok")
		case strings.HasPrefix(cmd, "MAIL FROM") || strings.HasPrefix(cmd, "RCPT TO"):
			writeSMTP(writer, "250 ok")
		case strings.HasPrefix(cmd, "DATA"):
			writeSMTP(writer, "354 end with dot")
			for {
				line, err := reader.ReadString('\n')
				if err != nil {
					return "", err
				}
				if strings.TrimSpace(line) == "." {
					break
				}
				data.WriteString(line)
			}
			writeSMTP(writer, "250 queued")
		case strings.HasPrefix(cmd, "QUIT"):
			writeSMTP(writer, "221 bye")
			return data.String(), nil
		default:
			writeSMTP(writer, "250 ok")
		}
	}
}

func writeSMTP(w *bufio.Writer, line string) {
	writeRawSMTP(w, line+"\r\n")
}

func writeRawSMTP(w *bufio.Writer, data string) {
	_, _ = w.WriteString(data)
	_ = w.Flush()
}

func testCertificate(t *testing.T) (tls.Certificate, *x509.CertPool) {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatalf("generate key: %v", err)
	}
	template := x509.Certificate{
		SerialNumber: big.NewInt(1),
		Subject:      pkix.Name{CommonName: "localhost"},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(time.Hour),
		DNSNames:     []string{"localhost"},
		IPAddresses:  []net.IP{net.IPv4(127, 0, 0, 1)},
		KeyUsage:     x509.KeyUsageDigitalSignature | x509.KeyUsageKeyEncipherment,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
	}
	der, err := x509.CreateCertificate(rand.Reader, &template, &template, &key.PublicKey, key)
	if err != nil {
		t.Fatalf("create cert: %v", err)
	}
	certPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
	keyPEM := pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(key)})
	cert, err := tls.X509KeyPair(certPEM, keyPEM)
	if err != nil {
		t.Fatalf("key pair: %v", err)
	}
	pool := x509.NewCertPool()
	if !pool.AppendCertsFromPEM(certPEM) {
		t.Fatalf("append cert")
	}
	return cert, pool
}
