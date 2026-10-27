package auth

import (
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"
	"time"
)

var key = []byte("0123456789abcdef0123456789abcdef")

func signed(uri string, at time.Time, nonce string, k []byte) *http.Request {
	r := httptest.NewRequest("GET", uri, nil)
	ts := strconv.FormatInt(at.Unix(), 10)
	r.Header.Set(HeaderTime, ts)
	r.Header.Set(HeaderNonce, nonce)
	r.Header.Set(HeaderSignature, Sign(k, "GET", uri, ts, nonce))
	return r
}

func TestCheck(t *testing.T) {
	now := time.Unix(1_800_000_000, 0)
	nonce := "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"

	v := NewVerifier(key)
	if err := v.Check(signed("/v1/snapshot", now, nonce, key), now); err != nil {
		t.Fatalf("valid request rejected: %v", err)
	}
	if err := v.Check(signed("/v1/snapshot", now, nonce, key), now); err != ErrReplay {
		t.Fatalf("replay: got %v", err)
	}

	other := "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
	if err := v.Check(signed("/v1/snapshot", now, other, []byte("wrong-key-wrong-key-wrong-key-xx")), now); err != ErrSignature {
		t.Fatalf("wrong key: got %v", err)
	}
	if err := v.Check(signed("/v1/snapshot", now.Add(-2*time.Minute), other, key), now); err != ErrClock {
		t.Fatalf("old timestamp: got %v", err)
	}

	// A signature for one path must not work for another.
	r := signed("/v1/snapshot", now, other, key)
	r.URL.Path = "/v1/history"
	if err := v.Check(r, now); err != ErrSignature {
		t.Fatalf("path swap: got %v", err)
	}

	r = signed("/v1/snapshot", now, "short", key)
	if err := v.Check(r, now); err != ErrMissing {
		t.Fatalf("bad nonce: got %v", err)
	}
}
