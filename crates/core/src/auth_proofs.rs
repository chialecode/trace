//! Remembers for a short while that a credential generation was found signed in, so the several
//! jobs of one batch (the pictures of one note, the entries of one selection) ask the site once
//! and not once each. The proof keeps the time it was made; a revoked or replaced credential
//! (a new generation names a new scope) never finds an old proof.
use std::collections::HashMap;
use std::sync::Mutex;
use std::time::{Duration, Instant};

pub(crate) struct AuthProofs {
    reuse: Duration,
    proved: Mutex<HashMap<String, (Instant, String)>>,
}

impl AuthProofs {
    pub(crate) fn new(reuse: Duration) -> Self {
        Self {
            reuse,
            proved: Mutex::new(HashMap::new()),
        }
    }

    /// When `scope` of `site` was proved, if that is recent enough to rely on.
    pub(crate) fn recent(&self, site: &str, scope: &str) -> Option<String> {
        let mut proved = self.proved.lock().unwrap();
        let key = format!("{site}/{scope}");
        match proved.get(&key) {
            Some((at, validated)) if at.elapsed() < self.reuse => Some(validated.clone()),
            Some(_) => {
                proved.remove(&key);
                None
            }
            None => None,
        }
    }

    pub(crate) fn note(&self, site: &str, scope: &str, validated_at: &str) {
        self.proved.lock().unwrap().insert(
            format!("{site}/{scope}"),
            (Instant::now(), validated_at.to_owned()),
        );
    }

    /// A credential of the site was revoked or replaced.
    pub(crate) fn forget(&self, site: &str) {
        let prefix = format!("{site}/");
        self.proved
            .lock()
            .unwrap()
            .retain(|key, _| !key.starts_with(&prefix));
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn a_proof_is_reused_only_while_recent_and_only_for_its_own_scope() {
        let proofs = AuthProofs::new(Duration::from_millis(80));
        assert!(proofs.recent("x", "cred/g1").is_none());
        proofs.note("x", "cred/g1", "2026-10-17T10:00:00Z");
        assert_eq!(
            proofs.recent("x", "cred/g1").as_deref(),
            Some("2026-10-17T10:00:00Z")
        );
        assert!(
            proofs.recent("x", "cred/g2").is_none(),
            "a new generation is a new scope"
        );
        assert!(proofs.recent("tiktok", "cred/g1").is_none());
        std::thread::sleep(Duration::from_millis(120));
        assert!(
            proofs.recent("x", "cred/g1").is_none(),
            "an old proof is not relied on"
        );
    }

    #[test]
    fn revoking_a_site_credential_drops_its_proofs_and_leaves_other_sites() {
        let proofs = AuthProofs::new(Duration::from_secs(60));
        proofs.note("x", "cred/g1", "t1");
        proofs.note("xiaohongshu", "cred/g1", "t2");
        proofs.forget("x");
        assert!(proofs.recent("x", "cred/g1").is_none());
        assert_eq!(
            proofs.recent("xiaohongshu", "cred/g1").as_deref(),
            Some("t2")
        );
    }
}
