package pgp

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/ProtonMail/go-crypto/openpgp/packet"
)

// PostKey submits one ASCII-armored public key to a single keyserver (AI.md
// 14183). keys.openpgp.org uses the VKS JSON API; classic HKP servers accept a
// form-encoded keytext at pks/add. This one definition is shared by the server's
// background publisher and the `--maintenance pgp publish` CLI so the submission
// protocol can never drift between them.
func PostKey(keyserver, pubArmored string) error {
	return PostKeyWithClient(keyserver, pubArmored, &http.Client{Timeout: 30 * time.Second})
}

// PostKeyWithClient submits a key using the caller's shared HTTP client. The
// server injects its configured outbound client so app-owned keyserver
// destinations follow the operator's Tor network setting.
func PostKeyWithClient(keyserver, pubArmored string, client *http.Client) error {
	if client == nil {
		return fmt.Errorf("pgp: HTTP client is nil")
	}
	if strings.Contains(keyserver, "vks/v1/upload") {
		payload, err := json.Marshal(map[string]string{"keytext": pubArmored})
		if err != nil {
			return err
		}
		resp, err := client.Post(keyserver, "application/json", bytes.NewReader(payload))
		if err != nil {
			return err
		}
		defer func() { _ = resp.Body.Close() }()
		if resp.StatusCode >= 300 {
			return fmt.Errorf("keyserver returned %d", resp.StatusCode)
		}
		return nil
	}
	endpoint := strings.TrimRight(keyserver, "/") + "/pks/add"
	resp, err := client.PostForm(endpoint, url.Values{"keytext": {pubArmored}})
	if err != nil {
		return err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode >= 300 {
		return fmt.Errorf("keyserver returned %d", resp.StatusCode)
	}
	return nil
}

// SignPublicKey cross-signs newPubArmored with the outgoing private key so that
// keyservers and clients can chain trust from the previous key to the rotated
// one (AI.md 14182: "signs the new pubkey with the old key"). oldPrivArmored is
// the already-unwrapped previous private key; the returned string is the new
// public key re-armored with the added certification signature.
func SignPublicKey(oldPrivArmored, newPubArmored string) (string, error) {
	signer, err := readEntity(oldPrivArmored)
	if err != nil {
		return "", err
	}
	if signer.PrivateKey == nil {
		return "", fmt.Errorf("pgp: signer key has no private material")
	}
	target, err := readEntity(newPubArmored)
	if err != nil {
		return "", err
	}
	cfg := &packet.Config{}
	for name := range target.Identities {
		if err := target.SignIdentity(name, signer, cfg); err != nil {
			return "", fmt.Errorf("pgp: cross-sign identity %q: %w", name, err)
		}
	}
	return armorEntity(target, false)
}

// PublicFromPrivate extracts the ASCII-armored public key from an armored
// private key. Used by `--maintenance pgp import` to derive the public key file
// after importing operator-supplied private key material (AI.md 14186).
func PublicFromPrivate(privArmored string) (string, error) {
	entity, err := readEntity(privArmored)
	if err != nil {
		return "", err
	}
	if entity.PrivateKey == nil {
		return "", fmt.Errorf("pgp: key has no private material")
	}
	return armorEntity(entity, false)
}

// KeyLifetime returns the key's creation and expiry timestamps parsed from its
// primary self-signature. ok is false when the key carries no expiry (in which
// case expires is the zero time). Used to seed DB metadata for imported keys.
func KeyLifetime(armored string) (created time.Time, expires time.Time, ok bool, err error) {
	entity, err := readEntity(armored)
	if err != nil {
		return time.Time{}, time.Time{}, false, err
	}
	created = entity.PrimaryKey.CreationTime
	for _, id := range entity.Identities {
		if id.SelfSignature != nil && id.SelfSignature.KeyLifetimeSecs != nil {
			secs := *id.SelfSignature.KeyLifetimeSecs
			return created, created.Add(time.Duration(secs) * time.Second), true, nil
		}
	}
	return created, time.Time{}, false, nil
}
