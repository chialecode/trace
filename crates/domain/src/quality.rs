//! 保存程度判定（INV-03、C27 的纯规则部分）。
//!
//! M1a 不做自动质量替换与回收；本模块只表达“本次版本所选必要资源是否
//! 齐全”。组（如同一分 P 的视频+音频）内任一必要成员缺失即不完整。

use crate::states::SaveProgress;

/// 单个必要资源的保存事实。
#[derive(Debug, Clone, Copy, PartialEq, Eq)]
pub struct ResourceSave {
    pub required: bool,
    pub has_verified_blob: bool,
    /// 订阅设置让这份资源保持未下载；一旦有了已验证副本就按已保存算。
    pub deferred: bool,
}

/// 由资源保存事实推断内容版本保存程度。
///
/// 按订阅设置未下载的必需资源不算缺口：其余必需资源都已保存时结果是 `Deferred`，
/// 而不是 `Complete`（它们没有保存），也不是 `Partial`/`Pending`（没有缺失或失败）。
pub fn save_progress(resources: &[ResourceSave]) -> SaveProgress {
    let required: Vec<&ResourceSave> = resources.iter().filter(|r| r.required).collect();
    if required.is_empty() {
        return SaveProgress::Unknown;
    }
    let open = required
        .iter()
        .filter(|r| !r.has_verified_blob && !r.deferred)
        .count();
    let held_back = required
        .iter()
        .filter(|r| !r.has_verified_blob && r.deferred)
        .count();
    let saved = required.iter().filter(|r| r.has_verified_blob).count();
    if open == 0 && held_back == 0 {
        SaveProgress::Complete
    } else if open == 0 {
        SaveProgress::Deferred
    } else if saved == 0 {
        SaveProgress::Pending
    } else {
        SaveProgress::Partial
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    fn r(required: bool, saved: bool) -> ResourceSave {
        ResourceSave {
            required,
            has_verified_blob: saved,
            deferred: false,
        }
    }

    fn held(required: bool, saved: bool) -> ResourceSave {
        ResourceSave {
            deferred: true,
            ..r(required, saved)
        }
    }

    #[test]
    fn audio_failure_keeps_entry_partial_not_complete() {
        // C27：音轨失败 → 部分成功保留但整稿不误完整。
        let resources = [
            r(true, true),  // 视频流
            r(true, false), // 音频流失败
            r(false, true), // 封面（非必要）
        ];
        assert_eq!(save_progress(&resources), SaveProgress::Partial);
    }

    #[test]
    fn all_required_saved_is_complete() {
        let resources = [r(true, true), r(true, true), r(false, false)];
        assert_eq!(save_progress(&resources), SaveProgress::Complete);
    }

    #[test]
    fn nothing_saved_is_pending() {
        let resources = [r(true, false), r(true, false)];
        assert_eq!(save_progress(&resources), SaveProgress::Pending);
    }

    #[test]
    fn unknown_when_no_required_resources_declared() {
        assert_eq!(save_progress(&[]), SaveProgress::Unknown);
        assert_eq!(save_progress(&[r(false, true)]), SaveProgress::Unknown);
    }

    #[test]
    fn a_video_left_undownloaded_by_the_subscription_is_neither_missing_nor_failed() {
        // Only the deferred video is open: not complete, but not a gap either.
        assert_eq!(save_progress(&[held(true, false)]), SaveProgress::Deferred);
        assert_eq!(
            save_progress(&[r(true, true), held(true, false)]),
            SaveProgress::Deferred
        );
    }

    #[test]
    fn a_real_gap_still_shows_as_pending_or_partial_next_to_a_deferred_video() {
        assert_eq!(
            save_progress(&[r(true, false), held(true, false)]),
            SaveProgress::Pending
        );
        assert_eq!(
            save_progress(&[r(true, true), r(true, false), held(true, false)]),
            SaveProgress::Partial
        );
    }

    #[test]
    fn a_deferred_resource_that_was_saved_anyway_counts_as_saved() {
        assert_eq!(save_progress(&[held(true, true)]), SaveProgress::Complete);
    }

    #[test]
    fn optional_deferred_resources_do_not_matter() {
        assert_eq!(
            save_progress(&[r(true, true), held(false, false)]),
            SaveProgress::Complete
        );
    }
}
