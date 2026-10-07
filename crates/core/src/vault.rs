//! 凭证 vault：Windows DPAPI current-user 加密（INV-10，m1a-runtime-contracts §4）。
//!
//! - 文件存放于用户私有目录，按 archive_id 隔离；目录 ACL 仅当前用户。
//! - 加密失败/ACL 失败一律拒绝保存，绝不降级明文。
//! - 恢复/导出/备份不包含 vault；恢复后显示需重新登录。

use crate::error::CoreError;
use std::path::{Path, PathBuf};

pub struct Vault {
    root: PathBuf,
}

impl Vault {
    pub fn new(root: PathBuf) -> Result<Self, CoreError> {
        std::fs::create_dir_all(&root)?;
        restrict_dir_acl(&root)?;
        Ok(Self { root })
    }

    fn path_for(&self, credential_ref: &str) -> PathBuf {
        // credential_ref 为 DB 保存的非敏感引用；白名单字符防止路径穿越（C15）。
        let safe: String = credential_ref
            .chars()
            .filter(|c| c.is_ascii_alphanumeric() || *c == '-' || *c == '_')
            .collect();
        self.root.join(format!("{safe}.bin"))
    }

    /// 加密并保存；返回用于 DB 的非敏感 vault_ref。
    pub fn store(&self, credential_ref: &str, secret: &[u8]) -> Result<(), CoreError> {
        if secret.is_empty() {
            return Err(CoreError::InvalidInput("empty secret".into()));
        }
        let path = self.path_for(credential_ref);
        let blob = dpapi_protect(secret)?;
        // 先写临时再改名，避免半写文件。
        let tmp = path.with_extension("tmp");
        std::fs::write(&tmp, &blob)?;
        std::fs::rename(&tmp, &path)?;
        Ok(())
    }

    /// 解密读取；短期只在内存中存在。
    pub fn load(&self, credential_ref: &str) -> Result<Vec<u8>, CoreError> {
        let path = self.path_for(credential_ref);
        let blob = std::fs::read(&path).map_err(|e| match e.kind() {
            std::io::ErrorKind::NotFound => CoreError::NotFound("credential".into()),
            _ => CoreError::Io(e),
        })?;
        dpapi_unprotect(&blob)
    }

    /// 撤销：删除加密文件；不影响历史档案（C29 边界）。
    pub fn remove(&self, credential_ref: &str) -> Result<(), CoreError> {
        let path = self.path_for(credential_ref);
        match std::fs::remove_file(&path) {
            Ok(()) => Ok(()),
            Err(e) if e.kind() == std::io::ErrorKind::NotFound => Ok(()),
            Err(e) => Err(CoreError::Io(e)),
        }
    }

    pub fn exists(&self, credential_ref: &str) -> bool {
        self.path_for(credential_ref).exists()
    }
}

/// Resolve the current opaque vault version. IDs remain stable across updates;
/// backups never include the encrypted bytes or inherited authorization.
pub async fn load_current(core: &crate::core::Core, id: &str) -> Result<Vec<u8>, CoreError> {
    let id = id.to_owned();
    let reference: String = core
        .db
        .read(move |c| {
            Ok(c.query_row(
                "SELECT vault_ref FROM credentials_state WHERE credential_id=?1",
                [id],
                |r| r.get(0),
            )?)
        })
        .await?;
    core.vault.load(&reference)
}

#[cfg(windows)]
fn dpapi_protect(plain: &[u8]) -> Result<Vec<u8>, CoreError> {
    use windows::Win32::Foundation::{HLOCAL, LocalFree};
    use windows::Win32::Security::Cryptography::*;

    let mut out = CRYPT_INTEGER_BLOB::default();
    unsafe {
        CryptProtectData(
            &CRYPT_INTEGER_BLOB {
                cbData: plain.len() as u32,
                pbData: plain.as_ptr() as *mut u8,
            },
            None,
            None,
            None,
            None,
            CRYPTPROTECT_UI_FORBIDDEN,
            &mut out,
        )
        .map_err(|e| CoreError::Internal(format!("DPAPI protect failed: {e}")))?;
    }
    let slice = unsafe { std::slice::from_raw_parts(out.pbData, out.cbData as usize) }.to_vec();
    unsafe {
        let _ = LocalFree(Some(HLOCAL(out.pbData.cast::<core::ffi::c_void>())));
    }
    Ok(slice)
}

#[cfg(windows)]
fn dpapi_unprotect(blob: &[u8]) -> Result<Vec<u8>, CoreError> {
    use windows::Win32::Foundation::{HLOCAL, LocalFree};
    use windows::Win32::Security::Cryptography::*;
    let mut out = CRYPT_INTEGER_BLOB::default();
    unsafe {
        CryptUnprotectData(
            &CRYPT_INTEGER_BLOB {
                cbData: blob.len() as u32,
                pbData: blob.as_ptr() as *mut u8,
            },
            None,
            None,
            None,
            None,
            CRYPTPROTECT_UI_FORBIDDEN,
            &mut out,
        )
        .map_err(|e| CoreError::Internal(format!("DPAPI unprotect failed: {e}")))?;
    }
    let slice = unsafe { std::slice::from_raw_parts(out.pbData, out.cbData as usize) }.to_vec();
    unsafe {
        let _ = LocalFree(Some(HLOCAL(out.pbData.cast::<core::ffi::c_void>())));
    }
    Ok(slice)
}

#[cfg(not(windows))]
fn dpapi_protect(_plain: &[u8]) -> Result<Vec<u8>, CoreError> {
    Err(CoreError::Unsupported("DPAPI requires Windows".into()))
}

#[cfg(not(windows))]
fn dpapi_unprotect(_blob: &[u8]) -> Result<Vec<u8>, CoreError> {
    Err(CoreError::Unsupported("DPAPI requires Windows".into()))
}

/// 目录 ACL 限制为当前用户+系统+管理员（Windows）；失败返回错误（不降级）。
#[cfg(windows)]
fn restrict_dir_acl(dir: &Path) -> Result<(), CoreError> {
    use windows::Win32::Foundation::{ERROR_SUCCESS, HLOCAL, LocalFree};
    use windows::Win32::Security::Authorization::{
        ConvertSidToStringSidW, ConvertStringSecurityDescriptorToSecurityDescriptorW,
        SDDL_REVISION_1, SE_FILE_OBJECT, SetNamedSecurityInfoW,
    };
    use windows::Win32::Security::{
        DACL_SECURITY_INFORMATION, GetSecurityDescriptorDacl, GetTokenInformation,
        PROTECTED_DACL_SECURITY_INFORMATION, PSECURITY_DESCRIPTOR, TOKEN_QUERY, TOKEN_USER,
        TokenUser,
    };
    use windows::Win32::System::Threading::{GetCurrentProcess, OpenProcessToken};
    use windows::core::PCWSTR;

    unsafe {
        // 当前用户 SID。
        let mut token = windows::Win32::Foundation::HANDLE::default();
        OpenProcessToken(GetCurrentProcess(), TOKEN_QUERY, &mut token)
            .map_err(|e| CoreError::Internal(format!("open token: {e}")))?;
        let mut len = 0u32;
        let _ = GetTokenInformation(token, TokenUser, None, 0, &mut len);
        let mut buf = vec![0u8; len as usize];
        GetTokenInformation(
            token,
            TokenUser,
            Some(buf.as_mut_ptr() as *mut _),
            len,
            &mut len,
        )
        .map_err(|e| CoreError::Internal(format!("token info: {e}")))?;
        let user = buf.as_ptr() as *const TOKEN_USER;
        let mut sid_str = windows::core::PWSTR::default();
        ConvertSidToStringSidW((*user).User.Sid, &mut sid_str)
            .map_err(|e| CoreError::Internal(format!("sid: {e}")))?;
        let sid = sid_str
            .to_string()
            .map_err(|e| CoreError::Internal(format!("{e}")))?;
        let _ = LocalFree(Some(HLOCAL(sid_str.0 as *mut core::ffi::c_void)));

        // 受保护 DACL：仅当前用户 + SYSTEM + Administrators。
        let sddl =
            windows::core::HSTRING::from(format!("D:P(A;;FA;;;{sid})(A;;FA;;;SY)(A;;FA;;;BA)"));
        let mut psd_ptr: PSECURITY_DESCRIPTOR = PSECURITY_DESCRIPTOR(core::ptr::null_mut());
        ConvertStringSecurityDescriptorToSecurityDescriptorW(
            PCWSTR(sddl.as_ptr()),
            SDDL_REVISION_1,
            &mut psd_ptr,
            None,
        )
        .map_err(|e| CoreError::Internal(format!("sddl convert: {e}")))?;
        // 自相对 SD：经 GetSecurityDescriptorDacl 解析，不读字段。
        let mut present = windows::core::BOOL::default();
        let mut dacl: *mut windows::Win32::Security::ACL = core::ptr::null_mut();
        let mut defaulted = windows::core::BOOL::default();
        GetSecurityDescriptorDacl(psd_ptr, &mut present, &mut dacl, &mut defaulted)
            .map_err(|e| CoreError::Internal(format!("get dacl: {e}")))?;
        let wide: Vec<u16> = dir
            .as_os_str()
            .to_string_lossy()
            .encode_utf16()
            .chain([0])
            .collect();
        let status = if present.as_bool() && !dacl.is_null() {
            SetNamedSecurityInfoW(
                PCWSTR(wide.as_ptr()),
                SE_FILE_OBJECT,
                DACL_SECURITY_INFORMATION | PROTECTED_DACL_SECURITY_INFORMATION,
                None,
                None,
                Some(dacl),
                None,
            )
        } else {
            ERROR_SUCCESS
        };
        let _ = LocalFree(Some(HLOCAL(psd_ptr.0)));
        if status != ERROR_SUCCESS {
            return Err(CoreError::Internal(format!("set acl failed: {status:?}")));
        }
        Ok(())
    }
}

#[cfg(not(windows))]
fn restrict_dir_acl(_dir: &Path) -> Result<(), CoreError> {
    // 非 Windows 开发机不运行产品路径；真实产品目标为 Windows。
    Ok(())
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    #[cfg(windows)]
    fn store_load_roundtrip_and_isolated_refs() {
        let dir = tempfile::tempdir().unwrap();
        let vault = Vault::new(dir.path().to_path_buf()).unwrap();
        vault.store("cred-1", b"SENTINEL-SECRET-VALUE").unwrap();
        let loaded = vault.load("cred-1").unwrap();
        assert_eq!(loaded, b"SENTINEL-SECRET-VALUE");
        // 引用白名单过滤：分隔符与点号被剥离，穿越被中和，文件只落在 vault 目录内。
        vault.store("../evil", b"x").unwrap();
        assert!(vault.exists("evil"), "sanitized ref expected");
        assert!(!dir.path().parent().unwrap().join("evil.bin").exists());
        vault.remove("cred-1").unwrap();
        assert!(!vault.exists("cred-1"));
    }

    #[test]
    #[cfg(windows)]
    fn encrypted_at_rest() {
        let dir = tempfile::tempdir().unwrap();
        let vault = Vault::new(dir.path().to_path_buf()).unwrap();
        vault.store("cred-2", b"PLAINTEXT-COOKIE-SENTINEL").unwrap();
        let raw = std::fs::read(vault.path_for("cred-2")).unwrap();
        let text = String::from_utf8_lossy(&raw).to_string();
        assert!(
            !text.contains("PLAINTEXT-COOKIE-SENTINEL"),
            "vault file must not contain plaintext"
        );
    }

    /// C15：vault 目录 DACL 为受保护 DACL，仅当前用户/SYSTEM/Administrators。
    #[test]
    #[cfg(windows)]
    fn vault_dir_acl_is_restricted() {
        use windows::Win32::Foundation::CloseHandle;
        use windows::Win32::Storage::FileSystem::{
            CreateFileW, FILE_FLAG_BACKUP_SEMANTICS, FILE_GENERIC_READ, FILE_SHARE_READ,
            FILE_SHARE_WRITE, OPEN_EXISTING,
        };
        use windows::core::PCWSTR;
        let dir = tempfile::tempdir().unwrap();
        let _vault = Vault::new(dir.path().to_path_buf()).unwrap();
        let wide: Vec<u16> = dir
            .path()
            .as_os_str()
            .to_string_lossy()
            .encode_utf16()
            .chain([0])
            .collect();
        let handle = unsafe {
            CreateFileW(
                PCWSTR(wide.as_ptr()),
                FILE_GENERIC_READ.0,
                FILE_SHARE_READ | FILE_SHARE_WRITE,
                None,
                OPEN_EXISTING,
                FILE_FLAG_BACKUP_SEMANTICS,
                None,
            )
        }
        .unwrap();
        let sddl = crate::ipc::server::read_dacl_sddl(handle).unwrap();
        let _ = unsafe { CloseHandle(handle) };
        let sid = crate::ipc::server::current_user_sid().unwrap();
        assert!(sddl.contains("D:P"), "protected DACL expected, got {sddl}");
        assert!(
            sddl.contains(&format!("(A;;FA;;;{sid})")),
            "user ACE expected, got {sddl}"
        );
        assert!(
            sddl.contains("(A;;FA;;;SY)"),
            "SYSTEM ACE expected, got {sddl}"
        );
        assert!(
            sddl.contains("(A;;FA;;;BA)"),
            "Administrators ACE expected, got {sddl}"
        );
    }
}
