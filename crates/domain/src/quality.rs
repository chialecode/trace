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
}

/// 由资源保存事实推断内容版本保存程度。
pub fn save_progress(resources: &[ResourceSave]) -> SaveProgress {
    let required: Vec<&ResourceSave> = resources.iter().filter(|r| r.required).collect();
    if required.is_empty() {
        return SaveProgress::Unknown;
    }
    let saved = required.iter().filter(|r| r.has_verified_blob).count();
    if saved == required.len() {
        SaveProgress::Complete
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
}
