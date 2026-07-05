//! Plugin signing (PRD §5 Phase 5: signed plugin package).
//!
//! An enterprise deployment can require that every plugin be signed by a
//! trusted publisher. Signatures are ed25519 over the raw module bytes. The
//! runtime verifies the signature against a set of trusted public keys
//! before the module is ever instantiated.

use ed25519_dalek::{Signature, Verifier, VerifyingKey};

#[derive(Debug, thiserror::Error)]
pub enum SigningError {
    #[error("malformed public key")]
    BadKey,
    #[error("malformed signature")]
    BadSignature,
    #[error("signature does not verify against any trusted key")]
    Untrusted,
}

/// A verifier holding the set of trusted publisher public keys.
#[derive(Default, Clone)]
pub struct SignatureVerifier {
    keys: Vec<VerifyingKey>,
}

impl SignatureVerifier {
    pub fn new() -> Self {
        Self::default()
    }

    /// Adds a trusted 32-byte ed25519 public key.
    pub fn trust_key(&mut self, key_bytes: &[u8]) -> Result<(), SigningError> {
        let arr: [u8; 32] = key_bytes.try_into().map_err(|_| SigningError::BadKey)?;
        let key = VerifyingKey::from_bytes(&arr).map_err(|_| SigningError::BadKey)?;
        self.keys.push(key);
        Ok(())
    }

    pub fn is_empty(&self) -> bool {
        self.keys.is_empty()
    }

    /// Verifies a 64-byte signature over `wasm` against any trusted key.
    pub fn verify(&self, wasm: &[u8], signature: &[u8]) -> Result<(), SigningError> {
        let sig_arr: [u8; 64] = signature.try_into().map_err(|_| SigningError::BadSignature)?;
        let sig = Signature::from_bytes(&sig_arr);
        for key in &self.keys {
            if key.verify(wasm, &sig).is_ok() {
                return Ok(());
            }
        }
        Err(SigningError::Untrusted)
    }
}

#[cfg(test)]
mod tests {
    use super::*;
    use ed25519_dalek::{Signer, SigningKey};

    fn keypair() -> SigningKey {
        // Deterministic test key (do NOT use fixed seeds in production).
        SigningKey::from_bytes(&[7u8; 32])
    }

    #[test]
    fn valid_signature_verifies() {
        let sk = keypair();
        let wasm = b"\0asm fake module bytes";
        let sig = sk.sign(wasm);

        let mut v = SignatureVerifier::new();
        v.trust_key(sk.verifying_key().as_bytes()).unwrap();
        assert!(v.verify(wasm, &sig.to_bytes()).is_ok());
    }

    #[test]
    fn tampered_module_is_rejected() {
        let sk = keypair();
        let sig = sk.sign(b"original");
        let mut v = SignatureVerifier::new();
        v.trust_key(sk.verifying_key().as_bytes()).unwrap();
        assert!(matches!(v.verify(b"tampered", &sig.to_bytes()), Err(SigningError::Untrusted)));
    }

    #[test]
    fn untrusted_signer_is_rejected() {
        let signer = keypair();
        let other = SigningKey::from_bytes(&[9u8; 32]);
        let wasm = b"module";
        let sig = signer.sign(wasm);

        let mut v = SignatureVerifier::new();
        v.trust_key(other.verifying_key().as_bytes()).unwrap(); // trust the WRONG key
        assert!(v.verify(wasm, &sig.to_bytes()).is_err());
    }
}
