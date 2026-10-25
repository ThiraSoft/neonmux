// Passkey (WebAuthn), SAS code and resume keys. The checks they must pass
// live in webauthn.go, pair.go and resume.go. Nothing here touches the DOM
// at import time: node runs sasCode in selftest.mjs.

import { b64u, bytes, concat, utf8 } from './wire.js';

const subtle = () => globalThis.crypto.subtle;

// sasCode is SASCode of pair.go: sha256("nmx-pair-v1" || token || certHash
// || devicePub), first 4 bytes big-endian, modulo 1 000 000, 6 digits.
export async function sasCode(token, certHash, devicePub) {
  const sum = new Uint8Array(await subtle().digest('SHA-256',
    concat(utf8('nmx-pair-v1'), bytes(token), bytes(certHash), bytes(devicePub))));
  const v = new DataView(sum.buffer).getUint32(0, false) % 1000000;
  return String(v).padStart(6, '0');
}

// createPasskey registers a passkey for one machine. The challenge is the
// nonce of the connection's Challenge. user.id is random so a second
// machine never replaces the passkey of the first one (same rp.id).
// Call it straight from a tap: iOS wants a user gesture.
export function createPasskey(nonce, machineName, deviceName) {
  const label = (machineName || 'neonmux').slice(0, 64);
  return navigator.credentials.create({
    publicKey: {
      rp: { id: location.hostname, name: 'neonmux' },
      user: {
        id: globalThis.crypto.getRandomValues(new Uint8Array(16)),
        name: label,
        displayName: (deviceName || 'iPhone') + ' on ' + label,
      },
      challenge: bytes(nonce),
      pubKeyCredParams: [{ type: 'public-key', alg: -7 }],
      authenticatorSelection: { residentKey: 'preferred', userVerification: 'required' },
      attestation: 'none',
      timeout: 60000,
    },
  }).then(cred => {
    const r = cred.response;
    const pub = typeof r.getPublicKey === 'function' ? r.getPublicKey() : null;
    const authData = typeof r.getAuthenticatorData === 'function' ? r.getAuthenticatorData() : null;
    if (!pub || !authData) throw new Error('This browser cannot create an ES256 passkey');
    return {
      credId: new Uint8Array(cred.rawId),
      pubKey: new Uint8Array(pub),
      authData: new Uint8Array(authData),
      clientData: new Uint8Array(r.clientDataJSON),
    };
  });
}

// regWire is the Registration of a Pair message (RegistrationWire).
export function regWire(reg, name) {
  return {
    cred_id: b64u(reg.credId),
    pub_key: b64u(reg.pubKey),
    auth_data: b64u(reg.authData),
    client_data: b64u(reg.clientData),
    name,
  };
}

// getAssertion signs the connection's nonce with the machine's passkey
// (Face ID) and returns the AssertionWire of an Auth message.
export function getAssertion(nonce, credId) {
  return navigator.credentials.get({
    publicKey: {
      challenge: bytes(nonce),
      rpId: location.hostname,
      allowCredentials: [{ type: 'public-key', id: bytes(credId) }],
      userVerification: 'required',
      timeout: 60000,
    },
  }).then(a => ({
    cred_id: b64u(a.rawId),
    auth_data: b64u(a.response.authenticatorData),
    client_data: b64u(a.response.clientDataJSON),
    sig: b64u(a.response.signature),
  }));
}

// newResumeKey makes the session key sent with Pair or Auth: ECDSA P-256,
// private key not extractable (kept as a CryptoKey in IndexedDB), public
// key as SPKI DER.
export async function newResumeKey() {
  const kp = await subtle().generateKey({ name: 'ECDSA', namedCurve: 'P-256' }, false, ['sign', 'verify']);
  const spki = new Uint8Array(await subtle().exportKey('spki', kp.publicKey));
  return { key: kp.privateKey, publicKey: kp.publicKey, spki };
}

// resumeSig signs "nmx-resume-v1" || challenge. WebCrypto returns ECDSA
// signatures as raw r||s (64 bytes), which is what resume.go verifies.
export async function resumeSig(key, nonce) {
  return new Uint8Array(await subtle().sign({ name: 'ECDSA', hash: 'SHA-256' }, key,
    concat(utf8('nmx-resume-v1'), bytes(nonce))));
}
