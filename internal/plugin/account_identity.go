package plugin

import (
	"encoding/json"
	"strings"
)

// accountIdentity is the human-facing identity CommandCode reports for one
// API key. The quota page already shows the mailbox; the predictor reuses the
// same fields so both surfaces name an account the same way. The other fields
// are fallbacks for accounts whose whoami response carries no email (some
// keys only report a handle/display name, and team accounts carry an org).
type accountIdentity struct {
	Email    string
	UserName string
	Name     string
	KeyName  string
	OrgLogin string
}

// Label renders the identity with email first, then the account handle/name,
// then the org login. The org prefix is kept when present so a team account
// stays distinguishable from a personal account with the same email.
func (i accountIdentity) Label() string {
	primary := firstNonEmpty(i.Email, i.UserName, i.Name, i.KeyName)
	org := strings.TrimSpace(i.OrgLogin)
	switch {
	case org == "":
		return primary
	case primary == "" || strings.EqualFold(primary, org):
		return org
	default:
		return org + " (" + primary + ")"
	}
}

type whoamiIdentityUser struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	Email       string `json:"email"`
	UserName    string `json:"userName"`
	KeyName     string `json:"keyName"`
	DisplayName string `json:"displayName"`
}

type whoamiIdentityOrg struct {
	ID    string `json:"id"`
	Login string `json:"login"`
}

// whoamiIdentityResponse tolerates both the top-level shape
// ({user,org}) and the {data:{user,org}} envelope seen in some CommandCode
// responses. Missing fields stay empty so callers can fall back to the key
// digest instead of inventing an identity.
type whoamiIdentityResponse struct {
	Success bool               `json:"success"`
	User    whoamiIdentityUser `json:"user"`
	Org     *whoamiIdentityOrg `json:"org"`
	Data    *struct {
		User whoamiIdentityUser `json:"user"`
		Org  *whoamiIdentityOrg `json:"org"`
	} `json:"data"`
}

func accountIdentityFromWhoami(raw []byte) accountIdentity {
	var payload whoamiIdentityResponse
	if len(raw) == 0 || json.Unmarshal(raw, &payload) != nil {
		return accountIdentity{}
	}
	user, org := payload.User, payload.Org
	if payload.Data != nil {
		if user.empty() {
			user = payload.Data.User
		}
		if org == nil {
			org = payload.Data.Org
		}
	}
	out := accountIdentity{
		Email:    strings.TrimSpace(user.Email),
		UserName: strings.TrimSpace(user.UserName),
		Name:     strings.TrimSpace(user.Name),
		KeyName:  strings.TrimSpace(user.KeyName),
	}
	if out.KeyName == "" {
		out.KeyName = strings.TrimSpace(user.DisplayName)
	}
	if org != nil {
		out.OrgLogin = strings.TrimSpace(org.Login)
	}
	return out
}

func (u whoamiIdentityUser) empty() bool {
	return u.ID == "" && u.Name == "" && u.Email == "" && u.UserName == "" &&
		u.KeyName == "" && u.DisplayName == ""
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if trimmed := strings.TrimSpace(value); trimmed != "" {
			return trimmed
		}
	}
	return ""
}
