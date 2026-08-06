package notify

import (
	"bytes"
	"context"
	"crypto/tls"
	"fmt"
	"io"
	"mime"
	"net"
	"net/mail"
	"net/smtp"
	"strings"
	"time"

	"github.com/sealofyou/cpa-quota-alert-plugin/internal/config"
)

type SMTPSender struct {
	cfg       config.SMTPConfig
	getenv    config.Getenv
	tlsConfig *tls.Config
	dialer    *net.Dialer
}

func NewSMTPSender(cfg config.SMTPConfig, getenv config.Getenv) *SMTPSender {
	if getenv == nil {
		getenv = func(string) string { return "" }
	}
	return &SMTPSender{cfg: cfg, getenv: getenv}
}

func (s *SMTPSender) Send(ctx context.Context, msg Message) error {
	if s == nil || !s.cfg.Enabled {
		return nil
	}
	if err := validateMessage(msg); err != nil {
		return err
	}
	if hasHeaderInjection(s.cfg.FromName) {
		return stableError("smtp_header_injection")
	}

	username := strings.TrimSpace(s.getenv(s.cfg.UsernameEnv))
	password := strings.TrimSpace(s.getenv(s.cfg.PasswordEnv))
	fromRaw := strings.TrimSpace(s.getenv(s.cfg.FromEnv))
	recipientsRaw := strings.TrimSpace(s.getenv(s.cfg.RecipientsEnv))
	if username == "" || password == "" || fromRaw == "" || recipientsRaw == "" {
		return stableError("smtp_runtime_config")
	}
	if hasHeaderInjection(username) || hasHeaderInjection(fromRaw) || hasHeaderInjection(recipientsRaw) {
		return stableError("smtp_header_injection")
	}

	from, err := mail.ParseAddress(fromRaw)
	if err != nil || from.Address == "" {
		return stableError("smtp_invalid_address")
	}
	if s.cfg.FromName != "" {
		from.Name = s.cfg.FromName
	}
	recipients, err := parseRecipients(recipientsRaw)
	if err != nil {
		return err
	}

	deadline := time.Now().Add(time.Duration(s.cfg.TimeoutSeconds) * time.Second)
	ctx, cancel := context.WithDeadline(ctx, deadline)
	defer cancel()
	effectiveDeadline := deadline
	if ctxDeadline, ok := ctx.Deadline(); ok && ctxDeadline.Before(effectiveDeadline) {
		effectiveDeadline = ctxDeadline
	}

	address := net.JoinHostPort(s.cfg.Host, fmt.Sprintf("%d", s.cfg.Port))
	conn, err := s.dial(ctx, address)
	if err != nil {
		return stableError("smtp_connect_failed")
	}
	defer conn.Close()
	_ = conn.SetDeadline(effectiveDeadline)

	if s.cfg.TLSMode == "implicit_tls" {
		tlsConn := tls.Client(conn, s.clientTLSConfig())
		if err := tlsConn.HandshakeContext(ctx); err != nil {
			return stableError("smtp_tls_failed")
		}
		conn = tlsConn
		_ = conn.SetDeadline(effectiveDeadline)
	}
	stopWatch := watchConnContext(ctx, conn)
	defer stopWatch()

	client, err := smtp.NewClient(conn, s.cfg.Host)
	if err != nil {
		return stableError("smtp_protocol_failed")
	}
	defer client.Close()

	if s.cfg.TLSMode == "starttls" {
		if err := client.Hello("localhost"); err != nil {
			return stableError("smtp_protocol_failed")
		}
		if ok, _ := client.Extension("STARTTLS"); !ok {
			return stableError("smtp_tls_failed")
		}
		if err := client.StartTLS(s.clientTLSConfig()); err != nil {
			return stableError("smtp_tls_failed")
		}
	}

	if err := client.Auth(smtp.PlainAuth("", username, password, s.cfg.Host)); err != nil {
		return stableError("smtp_auth_failed")
	}
	if err := client.Mail(from.Address); err != nil {
		return stableError("smtp_protocol_failed")
	}
	for _, recipient := range recipients {
		if err := client.Rcpt(recipient.Address); err != nil {
			return stableError("smtp_protocol_failed")
		}
	}
	writer, err := client.Data()
	if err != nil {
		return stableError("smtp_protocol_failed")
	}
	if _, err := writer.Write(buildSMTPMessage(from, recipients, msg)); err != nil {
		_ = writer.Close()
		return stableError("smtp_protocol_failed")
	}
	if err := writer.Close(); err != nil {
		return stableError("smtp_protocol_failed")
	}
	if err := client.Quit(); err != nil {
		return stableError("smtp_protocol_failed")
	}
	return nil
}

func watchConnContext(ctx context.Context, conn net.Conn) func() {
	stop := make(chan struct{})
	done := make(chan struct{})
	go func() {
		defer close(done)
		select {
		case <-ctx.Done():
			_ = conn.SetDeadline(time.Now())
			_ = conn.Close()
		case <-stop:
		}
	}()
	return func() {
		close(stop)
		<-done
	}
}

func (s *SMTPSender) dial(ctx context.Context, address string) (net.Conn, error) {
	dialer := s.dialer
	if dialer == nil {
		dialer = &net.Dialer{Timeout: time.Duration(s.cfg.TimeoutSeconds) * time.Second}
	}
	return dialer.DialContext(ctx, "tcp", address)
}

func (s *SMTPSender) clientTLSConfig() *tls.Config {
	base := &tls.Config{ServerName: s.cfg.Host, MinVersion: tls.VersionTLS12}
	if s.tlsConfig != nil {
		base = s.tlsConfig.Clone()
		base.MinVersion = tls.VersionTLS12
		if base.ServerName == "" {
			base.ServerName = s.cfg.Host
		}
	}
	return base
}

func parseRecipients(raw string) ([]*mail.Address, error) {
	if hasHeaderInjection(raw) {
		return nil, stableError("smtp_header_injection")
	}
	parts := strings.FieldsFunc(raw, func(r rune) bool { return r == ',' || r == ';' })
	out := make([]*mail.Address, 0, len(parts))
	for _, part := range parts {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		addr, err := mail.ParseAddress(part)
		if err != nil || addr.Address == "" || hasHeaderInjection(addr.Name) || hasHeaderInjection(addr.Address) {
			return nil, stableError("smtp_invalid_address")
		}
		out = append(out, addr)
	}
	if len(out) == 0 {
		return nil, stableError("smtp_invalid_address")
	}
	return out, nil
}

func buildSMTPMessage(from *mail.Address, recipients []*mail.Address, msg Message) []byte {
	var buf bytes.Buffer
	writeHeader(&buf, "From", from.String())
	to := make([]string, 0, len(recipients))
	for _, recipient := range recipients {
		to = append(to, recipient.String())
	}
	writeHeader(&buf, "To", strings.Join(to, ", "))
	writeHeader(&buf, "Subject", mime.QEncoding.Encode("utf-8", msg.Subject))
	writeHeader(&buf, "Date", occurredAt(msg).Format(time.RFC1123Z))
	writeHeader(&buf, "MIME-Version", "1.0")
	writeHeader(&buf, "Content-Type", "text/plain; charset=utf-8")
	writeHeader(&buf, "Content-Transfer-Encoding", "8bit")
	buf.WriteString("\r\n")
	_, _ = io.WriteString(&buf, msg.Body)
	if !strings.HasSuffix(msg.Body, "\n") {
		buf.WriteString("\r\n")
	}
	return buf.Bytes()
}

func writeHeader(buf *bytes.Buffer, key, value string) {
	buf.WriteString(key)
	buf.WriteString(": ")
	buf.WriteString(value)
	buf.WriteString("\r\n")
}
