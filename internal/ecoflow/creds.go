package ecoflow

import "strings"

// AllowedAPIHosts are the permitted EcoFlow API hosts.
var AllowedAPIHosts = map[string]bool{
	"api.ecoflow.com":   true,
	"api-e.ecoflow.com": true,
	"api-a.ecoflow.com": true,
}

// Credentials are the stored EcoFlow app credentials (persistence/encryption
// is the caller's concern). JSON keys match the Python payload.
type Credentials struct {
	Email        string `json:"email"`
	Password     string `json:"password"`
	APIHost      string `json:"api_host"`
	Token        string `json:"token"`
	UserID       string `json:"user_id"`
	MQTTClientID string `json:"mqtt_client_id"`
}

// Normalize trims fields and forces the API host to an allowed value.
func (c Credentials) Normalize() Credentials {
	host := strings.ToLower(strings.TrimSpace(c.APIHost))
	if host == "" || !AllowedAPIHosts[host] {
		host = DefaultAPIHost
	}
	return Credentials{
		Email: strings.TrimSpace(c.Email), Password: c.Password, APIHost: host,
		Token: c.Token, UserID: strings.TrimSpace(c.UserID),
		MQTTClientID: strings.TrimSpace(c.MQTTClientID),
	}
}

// Valid reports whether email and password are both set.
func (c Credentials) Valid() bool { return c.Email != "" && c.Password != "" }

// PublicView hides secrets (password/token) and adds has_password.
type PublicView struct {
	Credentials
	HasPassword bool `json:"has_password"`
}

// Public returns the redacted view.
func (c Credentials) Public() PublicView {
	pv := PublicView{Credentials: c, HasPassword: c.Password != ""}
	pv.Password, pv.Token = "", ""
	return pv
}

// MergeSave implements save(): an empty password keeps the previous one,
// an unspecified client id keeps the previous id, and token/user_id fall
// back to the existing session. ok=false when email or password is empty
// after merging.
func MergeSave(existing *Credentials, in Credentials, mqttClientIDSet bool) (Credentials, bool) {
	var ex Credentials
	if existing != nil {
		ex = *existing
	}
	password := in.Password
	if password == "" && existing != nil {
		password = ex.Password
	}
	clientID := in.MQTTClientID
	if !mqttClientIDSet && existing != nil {
		clientID = ex.MQTTClientID
	}
	host := in.APIHost
	if host == "" {
		host = ex.APIHost
	}
	token := in.Token
	if token == "" {
		token = ex.Token
	}
	uid := in.UserID
	if uid == "" {
		uid = ex.UserID
	}
	out := Credentials{
		Email: in.Email, Password: password, APIHost: host,
		Token: token, UserID: uid, MQTTClientID: clientID,
	}.Normalize()
	return out, out.Valid()
}

// WithSession returns c with the session fields updated (update_session);
// a nil mqttClientID keeps the stored one.
func (c Credentials) WithSession(token, userID string, mqttClientID *string) Credentials {
	c.Token, c.UserID = token, userID
	if mqttClientID != nil {
		c.MQTTClientID = *mqttClientID
	}
	return c.Normalize()
}
