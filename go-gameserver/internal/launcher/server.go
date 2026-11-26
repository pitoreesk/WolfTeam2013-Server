package launcher

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/tls"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"mime"
	"net"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

const (
	SessionPath      = "/api/v1/launcher/session"
	StartSessionPath = "/api/v1/launcher/session/start"

	defaultSessionTTL  = time.Minute
	sessionTokenBytes  = 32
	maxLoginBodyBytes  = 4096
	maxUsernameBytes   = 20
	maxPasswordBytes   = 64
	maxLauncherHeaders = 16 << 10
	maxPendingSessions = 4096
)

type Options struct {
	ChannelID   uint32
	TLSCertFile string
	TLSKeyFile  string
	SessionTTL  time.Duration
}

type Server struct {
	address       string
	options       Options
	authenticator Authenticator
	logger        *slog.Logger

	mu       sync.Mutex
	sessions map[[sha256.Size]byte]pendingSession
	now      func() time.Time
	random   io.Reader
}

type pendingSession struct {
	userID    int
	expiresAt time.Time
}

type Authenticator interface {
	Login(context.Context, string, string) (int, error)
}

type loginRequest struct {
	Username string `json:"username"`
	Password string `json:"password"`
}

type sessionResponse struct {
	Token     string `json:"token"`
	ExpiresAt string `json:"expiresAt"`
}

type startResponse struct {
	ChannelID uint32 `json:"channelId"`
}

type errorResponse struct {
	Error string `json:"error"`
}

func NewServer(address string, options Options, authenticator Authenticator, logger *slog.Logger) *Server {
	if options.SessionTTL <= 0 {
		options.SessionTTL = defaultSessionTTL
	}
	return &Server{
		address: address, options: options, authenticator: authenticator, logger: logger,
		sessions: make(map[[sha256.Size]byte]pendingSession), now: time.Now, random: rand.Reader,
	}
}

type prefixConn struct {
	net.Conn
	reader io.Reader
}

func (p *prefixConn) Read(b []byte) (int, error) {
	return p.reader.Read(b)
}

type chanListener struct {
	addr   net.Addr
	ch     chan net.Conn
	done   chan struct{}
	closed atomic.Bool
}

func newChanListener(addr net.Addr) *chanListener {
	return &chanListener{
		addr: addr,
		ch:   make(chan net.Conn),
		done: make(chan struct{}),
	}
}

func (l *chanListener) Accept() (net.Conn, error) {
	select {
	case conn, ok := <-l.ch:
		if !ok {
			return nil, net.ErrClosed
		}
		return conn, nil
	case <-l.done:
		return nil, net.ErrClosed
	}
}

func (l *chanListener) Close() error {
	if l.closed.CompareAndSwap(false, true) {
		close(l.done)
	}
	return nil
}

func (l *chanListener) Addr() net.Addr {
	return l.addr
}

func (s *Server) Run(ctx context.Context) error {
	listener, err := (&net.ListenConfig{}).Listen(ctx, "tcp", s.address)
	if err != nil {
		return fmt.Errorf("listen for launcher authentication requests: %w", err)
	}
	defer listener.Close()

	httpListener := newChanListener(listener.Addr())
	defer httpListener.Close()

	server := &http.Server{
		Handler:           s.handler(),
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       10 * time.Second,
		WriteTimeout:      10 * time.Second,
		IdleTimeout:       30 * time.Second,
		MaxHeaderBytes:    maxLauncherHeaders,
		TLSConfig:         &tls.Config{MinVersion: tls.VersionTLS12},
	}

	go func() {
		<-ctx.Done()
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = server.Shutdown(shutdownCtx)
		_ = httpListener.Close()
		_ = listener.Close()
	}()

	go func() {
		if s.options.TLSCertFile != "" {
			_ = server.ServeTLS(httpListener, s.options.TLSCertFile, s.options.TLSKeyFile)
		} else {
			_ = server.Serve(httpListener)
		}
	}()

	s.logger.Info("launcher dual-mode TCP/HTTP authentication service started", "address", listener.Addr().String())

	for {
		conn, err := listener.Accept()
		if err != nil {
			if ctx.Err() != nil || errors.Is(err, net.ErrClosed) {
				return nil
			}
			return err
		}

		go s.dispatchConn(ctx, conn, httpListener)
	}
}

func (s *Server) dispatchConn(ctx context.Context, conn net.Conn, httpListener *chanListener) {
	var prefix [2]byte
	_ = conn.SetReadDeadline(time.Now().Add(5 * time.Second))
	if _, err := io.ReadFull(conn, prefix[:]); err != nil {
		_ = conn.Close()
		return
	}
	_ = conn.SetReadDeadline(time.Time{})

	// Check if this is an ASCII HTTP request method
	b0, b1 := prefix[0], prefix[1]
	isHTTP := (b0 == 'P' && b1 == 'O') || // POST
		(b0 == 'G' && b1 == 'E') || // GET
		(b0 == 'O' && b1 == 'P') || // OPTIONS
		(b0 == 'H' && b1 == 'E') || // HEAD
		(b0 == 'D' && b1 == 'E') || // DELETE
		(b0 == 'P' && b1 == 'U') // PUT / PATCH

	if isHTTP {
		wrapped := &prefixConn{
			Conn:   conn,
			reader: io.MultiReader(bytes.NewReader(prefix[:]), conn),
		}
		select {
		case httpListener.ch <- wrapped:
		case <-httpListener.done:
			_ = conn.Close()
		case <-ctx.Done():
			_ = conn.Close()
		}
		return
	}

	// Binary launcher protocol (0x1310 etc.)
	s.handleBinaryConn(ctx, conn, prefix[:])
}

func (s *Server) handleBinaryConn(ctx context.Context, conn net.Conn, prefix []byte) {
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(15 * time.Second))

	frameSize := int(binary.LittleEndian.Uint16(prefix))
	if frameSize < HeaderSize || frameSize > MaxFrameSize {
		s.logger.Warn("invalid binary launcher frame size", "size", frameSize, "remote", conn.RemoteAddr())
		return
	}

	frame := make([]byte, frameSize)
	copy(frame[:2], prefix)
	if _, err := io.ReadFull(conn, frame[2:]); err != nil {
		s.logger.Warn("failed to read binary launcher frame", "error", err, "remote", conn.RemoteAddr())
		return
	}

	for {
		messageID, ok := MessageID(frame)
		if !ok {
			return
		}

		s.logger.Debug("launcher binary message received", "message_id", fmt.Sprintf("0x%04x", messageID), "remote", conn.RemoteAddr())

		switch messageID {
		case MessageLoginRequest:
			username, password, err := DecodeCredentials(frame)
			if err != nil {
				s.logger.Warn("failed to decode launcher credentials", "error", err, "remote", conn.RemoteAddr())
				_, _ = conn.Write(AuthResult(AuthWrongPassword))
				return
			}

			userID, err := s.authenticator.Login(ctx, username, password)
			if err != nil || userID < 0 {
				s.logger.Info("launcher binary login rejected", "username", username, "error", err, "remote", conn.RemoteAddr())
				_, _ = conn.Write(AuthResult(AuthWrongPassword))
				return
			}

			s.logger.Info("launcher binary login accepted", "username", username, "user_id", userID, "remote", conn.RemoteAddr())
			if _, err := conn.Write(Challenge(true)); err != nil {
				return
			}
			if _, err := conn.Write(AuthResult(0x10)); err != nil {
				return
			}

		case MessageSystemInfo:
			channelID := s.options.ChannelID
			if channelID == 0 {
				channelID = 0x13
			}
			s.logger.Info("launcher binary system info received, starting game", "channel_id", channelID, "remote", conn.RemoteAddr())
			if _, err := conn.Write(StartGame(channelID)); err != nil {
				return
			}

		default:
			s.logger.Debug("unknown launcher binary message", "message_id", fmt.Sprintf("0x%04x", messageID))
		}

		// Read next frame
		_ = conn.SetDeadline(time.Now().Add(30 * time.Second))
		var headerBuf [2]byte
		if _, err := io.ReadFull(conn, headerBuf[:]); err != nil {
			return
		}
		nextSize := int(binary.LittleEndian.Uint16(headerBuf[:]))
		if nextSize < HeaderSize || nextSize > MaxFrameSize {
			return
		}
		frame = make([]byte, nextSize)
		copy(frame[:2], headerBuf[:])
		if _, err := io.ReadFull(conn, frame[2:]); err != nil {
			return
		}
	}
}

func (s *Server) handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc(SessionPath, s.createSession)
	mux.HandleFunc(StartSessionPath, s.startSession)
	return http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		writer.Header().Set("Cache-Control", "no-store")
		writer.Header().Set("Pragma", "no-cache")
		writer.Header().Set("X-Content-Type-Options", "nosniff")
		mux.ServeHTTP(writer, request)
	})
}

func (s *Server) createSession(writer http.ResponseWriter, request *http.Request) {
	if request.Method != http.MethodPost {
		writer.Header().Set("Allow", http.MethodPost)
		writeJSON(writer, http.StatusMethodNotAllowed, errorResponse{Error: "method_not_allowed"})
		return
	}
	if s.authenticator == nil {
		s.logger.Error("launcher authenticator is not configured")
		writeJSON(writer, http.StatusServiceUnavailable, errorResponse{Error: "authentication_unavailable"})
		return
	}
	mediaType, _, err := mime.ParseMediaType(request.Header.Get("Content-Type"))
	if err != nil || mediaType != "application/json" {
		writeJSON(writer, http.StatusUnsupportedMediaType, errorResponse{Error: "application_json_required"})
		return
	}

	request.Body = http.MaxBytesReader(writer, request.Body, maxLoginBodyBytes)
	decoder := json.NewDecoder(request.Body)
	decoder.DisallowUnknownFields()
	var credentials loginRequest
	if err := decoder.Decode(&credentials); err != nil {
		writeJSON(writer, http.StatusBadRequest, errorResponse{Error: "invalid_request"})
		return
	}
	if err := requireJSONEnd(decoder); err != nil ||
		len(credentials.Username) < 1 || len(credentials.Username) > maxUsernameBytes ||
		len(credentials.Password) < 1 || len(credentials.Password) > maxPasswordBytes {
		writeJSON(writer, http.StatusBadRequest, errorResponse{Error: "invalid_request"})
		return
	}

	userID, err := s.authenticator.Login(request.Context(), credentials.Username, credentials.Password)
	if err != nil {
		s.logger.Error("launcher account authentication failed", "remote", request.RemoteAddr, "error", err)
		writeJSON(writer, http.StatusServiceUnavailable, errorResponse{Error: "authentication_unavailable"})
		return
	}
	if userID < 0 {
		s.logger.Info("launcher login rejected", "remote", request.RemoteAddr, "username", credentials.Username)
		writeJSON(writer, http.StatusUnauthorized, errorResponse{Error: "invalid_credentials"})
		return
	}

	token, expiresAt, err := s.issueSession(userID)
	if err != nil {
		s.logger.Error("create launcher session", "remote", request.RemoteAddr, "error", err)
		writeJSON(writer, http.StatusServiceUnavailable, errorResponse{Error: "authentication_unavailable"})
		return
	}
	s.logger.Info(
		"launcher login accepted",
		"remote", request.RemoteAddr,
		"username", credentials.Username,
		"user_id", userID,
	)
	writeJSON(writer, http.StatusCreated, sessionResponse{
		Token: token, ExpiresAt: expiresAt.UTC().Format(time.RFC3339),
	})
}

func (s *Server) startSession(writer http.ResponseWriter, request *http.Request) {
	if request.Method != http.MethodPost {
		writer.Header().Set("Allow", http.MethodPost)
		writeJSON(writer, http.StatusMethodNotAllowed, errorResponse{Error: "method_not_allowed"})
		return
	}
	token, ok := bearerToken(request.Header.Get("Authorization"))
	if !ok || !s.consumeSession(token) {
		writeJSON(writer, http.StatusUnauthorized, errorResponse{Error: "invalid_session"})
		return
	}
	writeJSON(writer, http.StatusOK, startResponse{ChannelID: s.options.ChannelID})
}

func (s *Server) issueSession(userID int) (string, time.Time, error) {
	raw := make([]byte, sessionTokenBytes)
	if _, err := io.ReadFull(s.random, raw); err != nil {
		return "", time.Time{}, fmt.Errorf("generate session token: %w", err)
	}
	token := base64.RawURLEncoding.EncodeToString(raw)
	digest := sha256.Sum256(raw)
	expiresAt := s.now().Add(s.options.SessionTTL)

	s.mu.Lock()
	defer s.mu.Unlock()
	now := s.now()
	for key, session := range s.sessions {
		if !session.expiresAt.After(now) {
			delete(s.sessions, key)
		}
	}
	if len(s.sessions) >= maxPendingSessions {
		return "", time.Time{}, errors.New("too many pending launcher sessions")
	}
	s.sessions[digest] = pendingSession{userID: userID, expiresAt: expiresAt}
	return token, expiresAt, nil
}

func (s *Server) consumeSession(token string) bool {
	raw, err := base64.RawURLEncoding.DecodeString(token)
	if err != nil || len(raw) != sessionTokenBytes {
		return false
	}
	digest := sha256.Sum256(raw)
	s.mu.Lock()
	defer s.mu.Unlock()
	session, ok := s.sessions[digest]
	if ok {
		delete(s.sessions, digest)
	}
	return ok && session.userID > 0 && session.expiresAt.After(s.now())
}

func bearerToken(value string) (string, bool) {
	const prefix = "Bearer "
	if !strings.HasPrefix(value, prefix) {
		return "", false
	}
	token := strings.TrimSpace(strings.TrimPrefix(value, prefix))
	return token, token != "" && !strings.ContainsAny(token, " \t\r\n")
}

func requireJSONEnd(decoder *json.Decoder) error {
	var extra any
	err := decoder.Decode(&extra)
	if errors.Is(err, io.EOF) {
		return nil
	}
	if err == nil {
		return errors.New("multiple JSON values")
	}
	return err
}

func writeJSON(writer http.ResponseWriter, status int, value any) {
	writer.Header().Set("Content-Type", "application/json; charset=utf-8")
	writer.WriteHeader(status)
	_ = json.NewEncoder(writer).Encode(value)
}
