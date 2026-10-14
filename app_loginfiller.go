package main

// loginFiller answers a VPN plugin's username / password / response
// prompts (the OpenVPN and Check Point plugins ask in that shape, see the
// plugins' PROTOCOL.md): the username from the profile or the password
// credential, the password from its credential, the response (a one-time
// code) from the configured source. Every stored source is used once per
// login; a refused login (error set) is asked in full, so a wrong stored
// value cannot lock the account through retries. OpenConnect forms are
// richer and have their own filler (ocAuthFiller).

import (
	"log"
	"strings"
	"sync"
	"time"

	sshlayer "ssh-tool/internal/ssh"
	"ssh-tool/internal/store"
	"ssh-tool/internal/totp"
	"ssh-tool/internal/tunnelhelper"
)

// loginSources is where a profile's login answers come from.
type loginSources struct {
	// Username "" = the password credential's default username, else asked.
	Username string
	// PasswordCredentialID "" = asked.
	PasswordCredentialID string
	// SecondPassword answers a challenge: ocSecondPrompt / ocSecondTOTP /
	// ocSecondCredential, with SecondCredentialID behind the last two.
	SecondPassword     string
	SecondCredentialID string
}

type loginFiller struct {
	a     *App
	row   *store.NetworkProfile
	tag   string // log prefix: the profile kind
	host  string // for the prompt dialog
	port  int
	login loginSources

	mu       sync.Mutex
	usedPass bool
}

func (f *loginFiller) handle(p *tunnelhelper.Proc, ch tunnelhelper.AuthChallenge) {
	values, missing := f.fill(ch)
	log.Printf("%s %s: %s", f.tag, f.row.Name, ocDescribeRound(ch, values, missing, nil))
	if len(missing) > 0 {
		if err := f.prompt(ch, values, missing); err != nil {
			_ = p.Cancel(ch.ID)
			return
		}
	}
	if err := p.Answer(ch.ID, values); err != nil {
		log.Printf("%s %s: answer: %v", f.tag, f.row.Name, err)
	}
}

// fill answers what the stored sources can; missing are the indexes of
// the fields left for the user.
func (f *loginFiller) fill(ch tunnelhelper.AuthChallenge) (map[string]string, []int) {
	values := map[string]string{}
	var missing []int
	f.mu.Lock()
	defer f.mu.Unlock()
	for i, fl := range ch.Fields {
		if ch.Error != "" && ch.Error != "reconnecting" {
			missing = append(missing, i)
			continue
		}
		switch fl.Name {
		case "username":
			if u := f.firstUser(); u != "" {
				values[fl.Key] = u
				continue
			}
		case "password":
			if !f.usedPass && f.login.PasswordCredentialID != "" {
				f.usedPass = true
				if s, err := f.a.ocCredentialSecret(f.login.PasswordCredentialID); err == nil {
					values[fl.Key] = s
					continue
				} else {
					log.Printf("%s %s: password credential: %v", f.tag, f.row.Name, err)
				}
			}
		case "response":
			if s := f.response(); s != "" {
				values[fl.Key] = s
				continue
			}
		}
		missing = append(missing, i)
	}
	return values, missing
}

func (f *loginFiller) firstUser() string {
	if f.login.Username != "" {
		return f.login.Username
	}
	return f.a.ocCredentialUser(f.login.PasswordCredentialID)
}

// response is the one-time code / challenge answer from the configured
// source; "" = ask.
func (f *loginFiller) response() string {
	switch f.login.SecondPassword {
	case ocSecondCredential:
		s, err := f.a.ocCredentialSecret(f.login.SecondCredentialID)
		if err != nil {
			log.Printf("%s %s: challenge credential: %v", f.tag, f.row.Name, err)
		}
		return s
	case ocSecondTOTP:
		cred, err := f.a.db.GetCredential(f.login.SecondCredentialID)
		if err != nil || cred == nil {
			return ""
		}
		sp, err := totp.Parse(f.a.externalTOTPSecret(cred))
		if err != nil {
			log.Printf("%s %s: one-time code: %v", f.tag, f.row.Name, err)
			return ""
		}
		return sp.Code(time.Now())
	}
	return ""
}

func (f *loginFiller) prompt(ch tunnelhelper.AuthChallenge, values map[string]string, missing []int) error {
	qs := make([]sshlayer.InteractiveAuthPrompt, len(missing))
	for j, i := range missing {
		fl := ch.Fields[i]
		text := fl.Label
		if text == "" {
			text = fl.Name + ":"
		}
		if fl.Name == "response" && !strings.HasSuffix(text, ":") {
			text += ":"
		}
		qs[j] = sshlayer.InteractiveAuthPrompt{Echo: fl.Kind != "password", Text: text}
	}
	instruction := ch.Message
	if ch.Error != "" {
		instruction = ch.Error + "\n" + instruction
	}
	answers, err := f.a.promptInteractiveAuth(f.row.Name, f.host, f.port, f.firstUser(), "VPN login", instruction, qs)
	if err != nil {
		return err
	}
	for j, i := range missing {
		if j < len(answers) {
			values[ch.Fields[i].Key] = answers[j]
		}
	}
	return nil
}
