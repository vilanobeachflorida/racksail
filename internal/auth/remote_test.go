package auth

import (
	"context"
	"encoding/json"
	"errors"
	"net/url"
	"strings"
	"testing"
)

// caller answers like an add-on would, through JSON.
type caller map[string]string

func (c caller) Call(ctx context.Context, method string, in, out any) error {
	if _, err := json.Marshal(in); err != nil {
		return err
	}
	answer, ok := c[method]
	if !ok {
		return errors.New("the add-on has no method " + method)
	}
	return json.Unmarshal([]byte(answer), out)
}

func TestRemoteRedirectProviders(t *testing.T) {
	ctx := context.Background()
	ip, err := RemoteIdentityProvider(ctx, caller{
		MethodDescribe: `{"method":"redirect"}`,
		MethodBegin:    `{"redirect":"https://id.example.com/authorize?state=x","state":"c2F2ZWQ="}`,
		MethodComplete: `{"identity":{"subject":"sub-1","username":"ada","groups":["staff"]}}`,
	})
	if err != nil {
		t.Fatal(err)
	}
	rp, ok := ip.(RedirectProvider)
	if !ok || ip.Method() != MethodRedirect {
		t.Fatalf("a redirect provider came back as %T", ip)
	}
	to, state, err := rp.Begin(ctx, "https://panel.example.com/callback")
	if err != nil || !strings.HasPrefix(to, "https://id.example.com/") || string(state) != "saved" {
		t.Fatalf("begin = %q %q %v", to, state, err)
	}
	id, err := rp.Complete(ctx, state, "https://panel.example.com/callback", url.Values{"code": {"c"}})
	if err != nil || id.Subject != "sub-1" || id.Groups[0] != "staff" {
		t.Errorf("complete = %+v, %v", id, err)
	}

	refused, _ := RemoteIdentityProvider(ctx, caller{
		MethodDescribe: `{"method":"redirect"}`,
		MethodComplete: `{"refused":true,"reason":"you aren't in a group that may sign in here"}`,
	})
	_, err = refused.(RedirectProvider).Complete(ctx, nil, "", nil)
	if !ErrProviderRefused.Is(err) || !strings.Contains(err.Error(), "group") {
		t.Errorf("a refusal = %v", err)
	}
	silent, _ := RemoteIdentityProvider(ctx, caller{MethodDescribe: `{"method":"redirect"}`, MethodComplete: `{}`})
	if _, err := silent.(RedirectProvider).Complete(ctx, nil, "", nil); err == nil {
		t.Error("an answer with neither identity nor refusal was taken")
	}
}

func TestRemotePasswordProviders(t *testing.T) {
	ctx := context.Background()
	ip, err := RemoteIdentityProvider(ctx, caller{
		MethodDescribe:      `{"method":"password"}`,
		MethodCheckPassword: `{"refused":true,"reason":"wrong password"}`,
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := ip.(PasswordProvider).CheckPassword(ctx, "ada", "x"); !ErrBadCredentials.Is(err) {
		t.Errorf("a refusal = %v", err)
	}
	if _, err := RemoteIdentityProvider(ctx, caller{MethodDescribe: `{"method":"carrier pigeon"}`}); err == nil {
		t.Error("a provider that signs people in some other way was taken")
	}
	if _, err := RemoteIdentityProvider(ctx, caller{}); err == nil {
		t.Error("a provider that can't describe itself was taken")
	}
}

func TestRemoteMFAMethods(t *testing.T) {
	ctx := context.Background()
	user := MFAUser{Username: "ada"}
	for result, want := range map[string]error{"passed": nil, "pending": ErrPending.New(), "failed": ErrBadCode.New()} {
		m := RemoteMFAMethod(caller{
			MethodChallenge: `{"prompt":"Type the code we sent you","input":true,"state":"b25l"}`,
			MethodVerify:    `{"result":"` + result + `"}`,
		})
		ch, err := m.Challenge(ctx, user)
		if err != nil || !ch.Input || string(ch.State) != "one" {
			t.Fatalf("challenge = %+v, %v", ch, err)
		}
		err = m.Verify(ctx, user, ch.State, "123456")
		if (want == nil) != (err == nil) || (want != nil && !errors.Is(err, want)) {
			t.Errorf("%s = %v", result, err)
		}
	}
	if err := RemoteMFAMethod(caller{MethodVerify: `{"result":"maybe"}`}).Verify(ctx, user, nil, ""); err == nil {
		t.Error("an answer that is neither passed, pending nor failed was taken")
	}
}
