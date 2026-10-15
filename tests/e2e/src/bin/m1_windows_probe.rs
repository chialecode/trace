//! Local Windows notification probe used by the synthetic startup test, never bundled.
fn main() {
    if std::env::args().nth(1).as_deref() != Some("--notify-synthetic") {
        eprintln!("use --notify-synthetic for one local, fixed-text notification");
        std::process::exit(2);
    }
    match probe() {
        Ok(()) => println!(
            "notification API and own-tag history verified, popup suppressed; visible delivery remains not-run"
        ),
        Err(error) => {
            eprintln!("{error}");
            std::process::exit(1);
        }
    }
}

#[cfg(windows)]
fn probe() -> Result<(), Box<dyn std::error::Error>> {
    use windows::{
        UI::Notifications::ToastNotificationManager,
        Win32::System::WinRT::{RO_INIT_MULTITHREADED, RoInitialize, RoUninitialize},
        core::HSTRING,
    };
    unsafe {
        RoInitialize(RO_INIT_MULTITHREADED)?;
    }
    struct Apartment;
    impl Drop for Apartment {
        fn drop(&mut self) {
            unsafe {
                RoUninitialize();
            }
        }
    }
    let _apartment = Apartment;
    let history = ToastNotificationManager::History()?;
    let app = HSTRING::from("TRACE.Archive.Probe");
    let tag = HSTRING::from("trace-probe");
    let group = HSTRING::from("trace-tests");
    // Inspect and remove only the probe tag, never unrelated notifications.
    let _ = history.RemoveGroupedTagWithId(&tag, &group, &app);
    trace_core::windows_integration::notify_probe()?;
    let deadline = std::time::Instant::now() + std::time::Duration::from_secs(10);
    loop {
        let entries = history.GetHistoryWithId(&app)?;
        for notification in entries {
            if notification.Tag()? == tag && notification.Group()? == group {
                let xml = notification.Content()?.GetXml()?.to_string();
                if !xml.contains("TRACE 档案状态")
                    || !xml.contains("activationType=\"protocol\"")
                    || !xml.contains("launch=\"trace-archive-probe:status\"")
                {
                    return Err("own notification content differs".into());
                }
                history.RemoveGroupedTagWithId(&tag, &group, &app)?;
                return Ok(());
            }
        }
        if std::time::Instant::now() >= deadline {
            return Err("own-tag notification missing from Windows history".into());
        }
        std::thread::sleep(std::time::Duration::from_millis(200));
    }
}

#[cfg(not(windows))]
fn probe() -> Result<(), Box<dyn std::error::Error>> {
    Err("Windows required".into())
}
