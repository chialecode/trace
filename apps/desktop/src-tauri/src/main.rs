#![cfg_attr(not(debug_assertions), windows_subsystem = "windows")]
#![cfg_attr(test, allow(clippy::too_many_lines))]

fn main() {
    trace_desktop_lib::run();
}
