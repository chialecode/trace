//! Stand-in for the locked media tools in tests: installed as `yt-dlp.exe` and `deno.exe` in a
//! temporary tools directory, it reports the locked versions and answers a format query with the
//! `info.json` next to it, recording the arguments it was given in `args.txt` and each such run
//! in `runs.txt`; a `fail.txt` makes it fail with that text. A run that asks for a download (it
//! gives a size limit) leaves `download.bin` next to it as `video.mp4` in its working directory.
use serde_json::Value;
use std::process::ExitCode;

const LOCK: &str = include_str!("../../../../media-tools.lock.json");

fn main() -> ExitCode {
    let lock: Value = serde_json::from_str(LOCK).expect("lock");
    let exe = std::env::current_exe().expect("exe");
    let dir = exe.parent().expect("dir");
    let name = exe
        .file_stem()
        .map(|n| n.to_string_lossy().to_lowercase())
        .unwrap_or_default();
    let args: Vec<String> = std::env::args().skip(1).collect();
    if args.iter().any(|a| a == "--version") {
        if name == "deno" {
            println!(
                "deno {} (stable, release, x86_64-pc-windows-msvc)",
                lock["deno"]["version"].as_str().unwrap_or_default()
            );
        } else {
            println!("{}", lock["yt_dlp"]["version"].as_str().unwrap_or_default());
        }
        return ExitCode::SUCCESS;
    }
    let _ = std::fs::write(dir.join("args.txt"), args.join("\n"));
    if let Ok(mut runs) = std::fs::OpenOptions::new()
        .create(true)
        .append(true)
        .open(dir.join("runs.txt"))
    {
        let _ = std::io::Write::write_all(&mut runs, b"run\n");
    }
    if let Ok(message) = std::fs::read_to_string(dir.join("fail.txt")) {
        eprintln!("{message}");
        return ExitCode::FAILURE;
    }
    if args.iter().any(|a| a == "--max-filesize") {
        let target = std::env::current_dir().expect("cwd").join("video.mp4");
        return match std::fs::copy(dir.join("download.bin"), target) {
            Ok(_) => ExitCode::SUCCESS,
            Err(_) => ExitCode::FAILURE,
        };
    }
    match std::fs::read_to_string(dir.join("info.json")) {
        Ok(text) => {
            print!("{text}");
            ExitCode::SUCCESS
        }
        Err(_) => ExitCode::FAILURE,
    }
}
