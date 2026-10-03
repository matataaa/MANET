package main

import (
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"net"
	"net/http"
	"sync"
	"time"
)

// Management logins. Each successful login gets its own random token; only
// its SHA-256 digest is kept, in memory, so a manet-ctrl restart or reboot
// ends every session. Lifetimes are measured with time.Since on time.Now()
// values, which use Go's monotonic clock, so a GPS/NTP clock step can't
// extend or cut short a session.
const (
	sessionLifetime = 48 * time.Hour
	maxSessions     = 64

	loginWindow       = time.Minute
	loginFailsPerAddr = 5
	loginFailsPerNode = 30
)

type session struct {
	created time.Time
	// Digest of the admin password the session was issued under: changing
	// the password (locally or by fleet push) invalidates older sessions.
	password [32]byte
}

type loginFailure struct {
	addr string
	at   time.Time
}

type sessionStore struct {
	mu       sync.Mutex
	sessions map[[32]byte]session
	// Failures inside loginWindow, oldest first. Throttled attempts are not
	// recorded, so this never holds more than loginFailsPerNode entries.
	failures []loginFailure
}

var sessions = &sessionStore{sessions: make(map[[32]byte]session)}

// create starts a session for password and returns its token.
func (s *sessionStore) create(password string) (string, error) {
	var raw [32]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return "", err
	}
	token := hex.EncodeToString(raw[:])

	s.mu.Lock()
	defer s.mu.Unlock()
	s.pruneLocked()
	for len(s.sessions) >= maxSessions {
		var oldestKey [32]byte
		var oldest time.Time
		for k, sess := range s.sessions {
			if oldest.IsZero() || sess.created.Before(oldest) {
				oldestKey, oldest = k, sess.created
			}
		}
		delete(s.sessions, oldestKey)
	}
	s.sessions[sha256.Sum256([]byte(token))] = session{
		created:  time.Now(),
		password: sha256.Sum256([]byte(password)),
	}
	return token, nil
}

// valid reports whether token is a live session issued under password.
func (s *sessionStore) valid(token, password string) bool {
	if token == "" || password == "" {
		return false
	}
	key := sha256.Sum256([]byte(token))
	pw := sha256.Sum256([]byte(password))

	s.mu.Lock()
	defer s.mu.Unlock()
	sess, ok := s.sessions[key]
	if !ok {
		return false
	}
	if time.Since(sess.created) >= sessionLifetime || sess.password != pw {
		delete(s.sessions, key)
		return false
	}
	return true
}

func (s *sessionStore) revoke(token string) {
	if token == "" {
		return
	}
	s.mu.Lock()
	delete(s.sessions, sha256.Sum256([]byte(token)))
	s.mu.Unlock()
}

func (s *sessionStore) pruneLocked() {
	for k, sess := range s.sessions {
		if time.Since(sess.created) >= sessionLifetime {
			delete(s.sessions, k)
		}
	}
}

// loginAllowed reports whether addr may attempt a login now; if not, it
// also returns how long until the oldest counted failure leaves the window.
func (s *sessionStore) loginAllowed(addr string) (bool, time.Duration) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.pruneFailuresLocked()

	if len(s.failures) >= loginFailsPerNode {
		return false, loginWindow - time.Since(s.failures[0].at)
	}
	var addrFails []time.Time
	for _, f := range s.failures {
		if f.addr == addr {
			addrFails = append(addrFails, f.at)
		}
	}
	if len(addrFails) >= loginFailsPerAddr {
		return false, loginWindow - time.Since(addrFails[0])
	}
	return true, 0
}

func (s *sessionStore) recordLoginFailure(addr string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.pruneFailuresLocked()
	s.failures = append(s.failures, loginFailure{addr: addr, at: time.Now()})
}

func (s *sessionStore) pruneFailuresLocked() {
	i := 0
	for i < len(s.failures) && time.Since(s.failures[i].at) >= loginWindow {
		i++
	}
	s.failures = s.failures[i:]
}

// passwordMatches compares in constant time; hashing first keeps the
// comparison independent of the submitted password's length.
func passwordMatches(submitted, expected string) bool {
	a := sha256.Sum256([]byte(submitted))
	b := sha256.Sum256([]byte(expected))
	return subtle.ConstantTimeCompare(a[:], b[:]) == 1
}

func clientAddr(r *http.Request) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}

func sessionToken(r *http.Request) string {
	if c, err := r.Cookie(PerfAuthCookie); err == nil {
		return c.Value
	}
	return ""
}
