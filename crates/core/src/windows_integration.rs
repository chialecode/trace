//! Small replaceable Windows notification/startup boundary using the locked
//! official windows bindings. No shell command evaluation or remote delivery.
use crate::{config::Profile, error::CoreError};
use sha2::{Digest, Sha256};
use std::io::Write;
use std::path::{Path, PathBuf};

/// Fixed command only: source URIs, notification text and credentials never
/// become process arguments. The protocol handler accepts no %1 substitution.
pub fn notification_command(exe: &Path, profile: &Path) -> Result<String, CoreError> {
    let mut paths = Vec::new();
    for path in [exe, profile] {
        let value = path
            .to_str()
            .ok_or_else(|| CoreError::InvalidInput("notification path encoding".into()))?;
        if !path.is_absolute()
            || value.contains(['"', '%', '\0', '\r', '\n'])
            || value.ends_with('\\')
        {
            return Err(CoreError::InvalidInput("notification path".into()));
        }
        paths.push(value);
    }
    let command = format!("\"{}\" --show-status --profile \"{}\"", paths[0], paths[1]);
    if command.encode_utf16().count() >= 32760 {
        return Err(CoreError::InvalidInput(
            "notification command too long".into(),
        ));
    }
    Ok(command)
}

/// A durable runtime profile lets a notification reopen the same archive even
/// after the daemon exits. It contains configuration, never vault contents.
/// Files are content-addressed and published without overwriting existing data.
pub fn notification_target(profile: &Profile) -> Result<(String, PathBuf), CoreError> {
    let mut saved = profile.clone();
    saved.data_root = std::path::absolute(&saved.data_root)?;
    saved.archive_root = std::path::absolute(&saved.archive_root)?;
    for path in [
        &mut saved.vault_dir,
        &mut saved.adapter_dir,
        &mut saved.media_dir,
        &mut saved.tools_dir,
    ]
    .into_iter()
    .flatten()
    {
        *path = std::path::absolute(&*path)?;
    }
    let bytes = serde_json::to_vec(&saved)?;
    let digest: String = Sha256::digest(&bytes)
        .iter()
        .map(|b| format!("{b:02x}"))
        .collect();
    let state = saved.data_root.join("state");
    // Core creates this state directory. A notification must never create an
    // unrelated archive or replace the user's original profile file.
    if !state.is_dir() {
        return Err(CoreError::InvalidInput(
            "notification state unavailable".into(),
        ));
    }
    let path = state.join(format!("notification-profile-{digest}.json"));
    if !path.try_exists()? {
        let mut temp = tempfile::Builder::new()
            .prefix(".notification-")
            .tempfile_in(&state)?;
        temp.write_all(&bytes)?;
        temp.as_file().sync_all()?;
        if let Err(error) = temp.persist_noclobber(&path)
            && error.error.kind() != std::io::ErrorKind::AlreadyExists
        {
            return Err(CoreError::Io(error.error));
        }
    }
    let metadata = std::fs::symlink_metadata(&path)?;
    #[cfg(windows)]
    let linked = {
        use std::os::windows::fs::MetadataExt;
        metadata.file_attributes() & 0x400 != 0
    };
    #[cfg(not(windows))]
    let linked = metadata.file_type().is_symlink();
    if linked
        || !metadata.is_file()
        || metadata.len() != bytes.len() as u64
        || std::fs::read(&path)? != bytes
    {
        return Err(CoreError::InvalidInput(
            "notification profile differs".into(),
        ));
    }
    let identity = serde_json::to_vec(&(&saved.archive_id, &saved.data_root))?;
    let hash: String = Sha256::digest(identity)
        .iter()
        .map(|b| format!("{b:02x}"))
        .collect();
    Ok((format!("trace-archive-{}", &hash[..32]), path))
}

pub fn startup_command(exe: &Path, profile: &Path) -> Result<String, CoreError> {
    let exe = exe
        .to_str()
        .ok_or_else(|| CoreError::InvalidInput("executable path encoding".into()))?;
    let profile = profile
        .to_str()
        .ok_or_else(|| CoreError::InvalidInput("profile path encoding".into()))?;
    if [exe, profile]
        .iter()
        .any(|p| p.contains(['"', '\0', '\r', '\n']) || p.ends_with('\\'))
    {
        return Err(CoreError::InvalidInput("startup path".into()));
    }
    let command = format!("\"{exe}\" --background-only --profile \"{profile}\"");
    if command.encode_utf16().count() > 260 {
        return Err(CoreError::InvalidInput(
            "Windows logon command exceeds 260 characters; shorten package/profile location".into(),
        ));
    }
    Ok(command)
}

#[cfg(windows)]
mod native {
    use super::*;
    use windows::{
        Win32::System::Registry::*,
        core::{HSTRING, PCWSTR},
    };
    struct Key(HKEY);
    impl Drop for Key {
        fn drop(&mut self) {
            unsafe {
                let _ = RegCloseKey(self.0);
            }
        }
    }
    fn key(path: &str) -> Result<Key, CoreError> {
        let mut key = HKEY::default();
        unsafe {
            RegCreateKeyExW(
                HKEY_CURRENT_USER,
                &HSTRING::from(path),
                None,
                PCWSTR::null(),
                REG_OPTION_NON_VOLATILE,
                KEY_READ | KEY_SET_VALUE,
                None,
                &mut key,
                None,
            )
            .ok()
        }
        .map_err(|_| CoreError::Internal("Windows user settings unavailable".into()))?;
        Ok(Key(key))
    }
    fn set(key: &Key, name: &str, value: &str) -> Result<(), CoreError> {
        let bytes: Vec<u8> = value
            .encode_utf16()
            .chain([0])
            .flat_map(u16::to_le_bytes)
            .collect();
        unsafe { RegSetValueExW(key.0, &HSTRING::from(name), None, REG_SZ, Some(&bytes)).ok() }
            .map_err(|_| CoreError::Internal("Windows user settings write failed".into()))
    }
    const RUN: &str = "Software\\Microsoft\\Windows\\CurrentVersion\\Run";
    pub fn startup(name: &str, value: Option<&str>) -> Result<(), CoreError> {
        let key = key(RUN)?;
        if let Some(value) = value {
            set(&key, name, value)
        } else {
            let status = unsafe { RegDeleteValueW(key.0, &HSTRING::from(name)) };
            if status == windows::Win32::Foundation::ERROR_FILE_NOT_FOUND {
                return Ok(());
            }
            status
                .ok()
                .map_err(|_| CoreError::Internal("Windows startup removal failed".into()))
        }
    }
    pub fn startup_value(name: &str) -> Result<Option<String>, CoreError> {
        let mut bytes = vec![0u16; 32768];
        let mut size = (bytes.len() * 2) as u32;
        let status = unsafe {
            RegGetValueW(
                HKEY_CURRENT_USER,
                &HSTRING::from(RUN),
                &HSTRING::from(name),
                RRF_RT_REG_SZ,
                None,
                Some(bytes.as_mut_ptr().cast()),
                Some(&mut size),
            )
        };
        if status == windows::Win32::Foundation::ERROR_FILE_NOT_FOUND {
            return Ok(None);
        }
        status
            .ok()
            .map_err(|_| CoreError::Internal("Windows startup query failed".into()))?;
        Ok(Some(String::from_utf16_lossy(
            &bytes[..(size as usize / 2).saturating_sub(1)],
        )))
    }
    pub fn notify(profile: &Profile, event: &str) -> Result<(), CoreError> {
        let exe = std::env::current_exe()?.with_file_name("trace-desktop.exe");
        if !exe.is_file() {
            return Err(CoreError::InvalidInput(
                "notification desktop unavailable".into(),
            ));
        }
        let (scheme, profile_path) = notification_target(profile)?;
        let command = notification_command(&exe, &profile_path)?;
        let handler = key(&format!("Software\\Classes\\{scheme}"))?;
        set(&handler, "", "URL:TRACE archive status")?;
        set(&handler, "URL Protocol", "")?;
        let open = key(&format!(
            "Software\\Classes\\{scheme}\\shell\\open\\command"
        ))?;
        set(&open, "", &command)?;
        show("TRACE.Archive", &format!("{scheme}:status"), event, false)
    }

    /// The automated probe uses a separate app ID and no protocol registration;
    /// it suppresses the popup and removes only its own history entry.
    pub fn notify_probe() -> Result<(), CoreError> {
        show(
            "TRACE.Archive.Probe",
            "trace-archive-probe:status",
            "test",
            true,
        )
    }

    fn show(
        app_id: &str,
        launch: &str,
        event: &str,
        suppress_popup: bool,
    ) -> Result<(), CoreError> {
        // Unpackaged Win32 app registration, scoped to this Windows user.
        let registration = key(&format!("Software\\Classes\\AppUserModelId\\{app_id}"))?;
        set(&registration, "DisplayName", "TRACE")?;
        // A stable stub CLSID supports persistence with protocol activation.
        // Foreground activation would require a real COM activation server.
        set(
            &registration,
            "CustomActivator",
            "{3947D861-57AC-4F6A-ACDF-57F50A2725D0}",
        )?;
        use windows::Data::Xml::Dom::XmlDocument;
        use windows::UI::Notifications::{ToastNotification, ToastNotificationManager};
        let mut step = "initialize";
        let result = (|| -> windows::core::Result<()> {
            use windows::Win32::System::WinRT::{
                RO_INIT_MULTITHREADED, RoInitialize, RoUninitialize,
            };
            struct Apartment(bool);
            impl Drop for Apartment {
                fn drop(&mut self) {
                    if self.0 {
                        unsafe {
                            RoUninitialize();
                        }
                    }
                }
            }
            let initialized = unsafe { RoInitialize(RO_INIT_MULTITHREADED) };
            if let Err(error) = &initialized {
                // An existing STA is usable too; other initialization failures
                // retain their numeric HRESULT without leaking system messages.
                if error.code() != windows::Win32::Foundation::RPC_E_CHANGED_MODE {
                    return Err(error.clone());
                }
            }
            let _apartment = Apartment(initialized.is_ok());
            step = "xml";
            let xml = XmlDocument::new()?;
            let body = if event == "resolved" {
                "采集问题已恢复。可在后台状态查看处理记录。"
            } else {
                "采集需要处理。请打开 TRACE 后台状态，查看受影响的订阅、凭证或存储。"
            };
            xml.LoadXml(&HSTRING::from(format!("<toast activationType=\"protocol\" launch=\"{launch}\"><visual><binding template=\"ToastGeneric\"><text>TRACE 档案状态</text><text>{body}</text></binding></visual></toast>")))?;
            step = "toast";
            let toast = ToastNotification::CreateToastNotification(&xml)?;
            if suppress_popup {
                toast.SetSuppressPopup(true)?;
                toast.SetTag(&HSTRING::from("trace-probe"))?;
                toast.SetGroup(&HSTRING::from("trace-tests"))?;
            }
            {
                step = "notifier";
                let notifier =
                    ToastNotificationManager::CreateToastNotifierWithId(&HSTRING::from(app_id))?;
                step = "setting";
                match notifier.Setting() {
                    Ok(setting)
                        if setting != windows::UI::Notifications::NotificationSetting::Enabled =>
                    {
                        return Err(windows::core::Error::from_hresult(
                            windows::Win32::Foundation::E_ACCESSDENIED,
                        ));
                    }
                    // Windows has no per-app preference before the first Show.
                    Err(e) if e.code().0 as u32 != 0x80070490 => return Err(e),
                    _ => {}
                }
                step = "show";
                notifier.Show(&toast)
            }
        })();
        result.map_err(|e| {
            CoreError::Internal(format!(
                "system notification unavailable at {step} (HRESULT {:08x})",
                e.code().0 as u32
            ))
        })
    }
}
#[cfg(windows)]
pub use native::{notify, notify_probe, startup, startup_value};
#[cfg(not(windows))]
pub fn notify(_: &Profile, _: &str) -> Result<(), CoreError> {
    Err(CoreError::Unsupported("Windows notifications".into()))
}
#[cfg(not(windows))]
pub fn startup(_: &str, _: Option<&str>) -> Result<(), CoreError> {
    Err(CoreError::Unsupported("Windows startup".into()))
}
#[cfg(not(windows))]
pub fn startup_value(_: &str) -> Result<Option<String>, CoreError> {
    Err(CoreError::Unsupported("Windows startup".into()))
}

#[cfg(test)]
mod tests {
    use super::*;
    #[test]
    fn notification_fixed_activation_preserves_profile_and_rejects_injection() {
        let root = tempfile::tempdir().unwrap();
        let profile = Profile {
            archive_id: "synthetic-notification".into(),
            data_root: root.path().join("data"),
            archive_root: root.path().join("archive"),
            vault_dir: Some(root.path().join("vault")),
            ..Default::default()
        };
        std::fs::create_dir_all(profile.data_root.join("state")).unwrap();
        let (scheme, saved) = notification_target(&profile).unwrap();
        assert!(scheme.starts_with("trace-archive-"));
        assert!(
            scheme
                .chars()
                .all(|c| c.is_ascii_lowercase() || c.is_ascii_digit() || c == '-')
        );
        assert_eq!(
            notification_target(&profile).unwrap(),
            (scheme.clone(), saved.clone())
        );
        let loaded = crate::config::load_profile(&saved).unwrap();
        assert_eq!(loaded.pipe_name(), profile.pipe_name());
        assert_eq!(loaded.data_root, profile.data_root);
        assert_eq!(loaded.archive_root, profile.archive_root);
        assert_eq!(loaded.vault_dir, profile.vault_dir);
        assert!(!profile.vault_dir.as_ref().unwrap().exists());
        let exe = root.path().join("package with spaces/trace-desktop.exe");
        let command = notification_command(&exe, &saved).unwrap();
        assert_eq!(
            command,
            format!(
                "\"{}\" --show-status --profile \"{}\"",
                exe.display(),
                saved.display()
            )
        );
        assert!(!command.contains("%1") && !command.contains("cmd.exe"));
        for bad in [
            "relative.exe",
            "bad\" --profile secret",
            "bad%1.exe",
            "bad\n.exe",
        ] {
            assert!(notification_command(Path::new(bad), &saved).is_err());
        }
        assert!(notification_command(&exe, &root.path().join("bad%1.json")).is_err());
        let mut other = profile.clone();
        other.archive_id = "other-archive".into();
        assert_ne!(notification_target(&other).unwrap().0, scheme);
        std::fs::write(&saved, b"damaged").unwrap();
        assert!(notification_target(&profile).is_err());
        assert_eq!(std::fs::read(&saved).unwrap(), b"damaged");
    }

    #[test]
    fn startup_quotes_paths_without_shell_execution() {
        let value = startup_command(
            Path::new(r"C:\Example Archive\trace-desktop.exe"),
            Path::new(r"C:\Synthetic Profiles\合成\profile.json"),
        )
        .unwrap();
        assert_eq!(
            value,
            "\"C:\\Example Archive\\trace-desktop.exe\" --background-only --profile \"C:\\Synthetic Profiles\\合成\\profile.json\""
        );
        assert!(startup_command(Path::new("bad\" --inject"), Path::new("p")).is_err());
        assert!(startup_command(Path::new("a.exe"), Path::new("p\0")).is_err());
    }
}
