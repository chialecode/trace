#!/usr/bin/env python3
"""TRACE-M1-01 evidence: command receipts and fail-closed aggregation.

Raw output is kept in artifacts, including failures. A receipt never upgrades
site or product acceptance. Source inputs include tests, build scripts and locks.
"""
from __future__ import annotations
import argparse
import hashlib
import json
import math
import os
import re
import subprocess
import sys
import time
from pathlib import Path
from benchmark_metrics import derive

ROOT=Path(__file__).resolve().parents[1]
PLAN_ID='TRACE-M1-01 r3'
SCHEMA_VERSION=6
# Explicit minimum semantics, not merely a total number of green tests. Missing
# names intentionally reject M1 while the associated implementation is absent.
REQUIRED_CASES={
    'C01': ['c01_collection_membership_is_preserved_per_entry_observation',
            'c01_site_check_dry_run_does_not_read_credentials_or_create_output','c01_range_is_inclusive_and_does_not_infer_order_or_unknown_dates',
            'c01_a_bogus_fixed_upstream_vector_and_identity_binding',
            'c01_collections_keep_independent_bounded_cursors_and_reject_incomplete_pages',
            'c01_pinned_old_video_cannot_hide_later_recent_or_unknown_dates',
            'c01_profile_scope_transfers_between_enabled_subscriptions_without_late_commit',
            'c01_c06_validation_counts_each_redirect_and_stops_without_secrets',
            'c01_site_check_rejects_unbounded_and_write_operations_before_network',
            'c01_identity_alias_repost_type_collision_and_persistent_cursor_cycle'],
    'C02': ['c02_article_ops_preserve_lines_styles_images_and_fail_closed',
            'c02_article_ops_av_mapping_and_opus_formula_list_production','c02_summary_promotion_stats_and_atomic_resource_jobs',
            'c01_c02_douyin_article_account_to_full_document_through_production_process',
            'c02_bilibili_article_full_body_and_legacy_protocol_defaults',
            'c02_bilibili_music_cards_require_full_audio_and_preserve_parent_on_trial',
            'c02_list_images_preserve_text_numbering_and_required_positions',
            'c02_table_images_preserve_cell_text_positions_and_required_resources',
            'c02_music_and_motion_edits_change_identity_but_signed_url_rotation_does_not',
            'c02_gallery_live_photo_required_tracks_and_edit_resources'],
    'C03': ['f007_chunked_lengths_and_success_statuses_are_exact',
            'a_f008_segments_must_keep_declared_size_validation',
            'f008_segment_sizes_hashes_and_legacy_aggregate_gate_completion',
            'f007_stream_over_thirty_seconds_and_cancellation',
            'a_f007_chunked_range_must_reach_declared_total'],
    'C04': ['c04_strict_quality_gap_survives_recovery_and_fallback_keeps_measured_media','c04_c07_disk_reserve_tracks_concurrent_minute_and_replacement_peak','c04_key_none_inheritance_ranges_map_and_discontinuity',
            'c04_real_ts_ingestion_dedup_and_missing_window_evidence',
            'c04_flv_partial_tags_corrupt_sizes_and_unbounded_headers',
            'c04_flv_recording_rotation_clock_codec_sleep_and_disk_full',
            'c04_closed_capture_four_commit_windows_survive_cancel_and_restart',
            'c04_local_identity_upgrade_and_new_broadcast_preserve_stop_and_history',
            'c04_c06_live_auxiliary_dedup_budget_pause_and_secrets',
            'c04_recording_commit_windows_and_independent_hls_audio'],
    'C05': ['c05_notification_fixed_activation_preserves_profile_and_rejects_injection','c05_background_launcher_double_start_single_daemon_and_invalid_profile','c05_windows_notification_history_with_suppressed_popup','c05_startup_quotes_paths_without_shell_execution','c04_c05_same_session_manual_stop_offline_and_late_generation',
            'f015_pause_due_running_resume_idempotency_and_restart',
            'a_u02_subscription_resume_must_resume_scan',
            'f005_f006_f007_task_windows_restart_pause_shutdown_and_cancel',
            'c05_local_maintenance_cancellation_and_plugin_independence',
            'c05_proxy_completion_cannot_cancel_partial_frame_read',
            'c05_all_job_types_plugin_isolation_and_shutdown_sealing'],
    'C06': ['c06_credential_update_rebind_generation_background_retry_and_quality_recovery','c06_health_merges_related_jobs_recovers_and_preserves_delivery_failures','c06_authentication_is_not_media_permission_or_network_health','c06_site_check_budget_applies_to_real_http_redirect_hops','c06_nested_secrets_and_signed_urls_do_not_reach_archive',
            'c06_local_backend_rejects_disguised_playlists_without_network',
            'c06_credential_scopes_and_cross_origin_redirect_strip_headers',
            'c06_nested_hls_private_network_key_map_and_credential_scopes'],
    'C07': ['c07_schema3_source_search_rebuild_preserves_old_body_and_snapshot','c07_deleted_identity_rescan_and_backup_never_silently_reinclude','c07_deletion_scopes_stale_preview_shared_bytes_and_explicit_restore','c07_strict_filters_each_representation_and_requires_all_necessary_media','c07_schema3_backup_migration_and_failed_migration_rollback',
            'c07_recovery_bounds_committed_cleanup_writes_and_keeps_objects',
            'c07_acquisition_identity_separates_spec_and_integrity_but_not_signed_urls',
            'c07_export_all_pages_versions_accounts_resources_and_failure_manifest',
            'c07_real_media_probe_keeps_each_variants_own_spec_and_upgrades_selection',
            'c07_quality_failure_shared_blob_pins_unique_coverage_and_gc_windows'],
    'C08': ['c01_c08_public_search_pages_full_scope_and_creator_edits_keep_identity','c08_public_search_forwards_shared_scope_and_continuation','c08_module_stop_shows_draining_until_workers_finish','c08_playback_cache_clear_rebuild_budget_and_originals','c08_resource_inventory_pages_variants_missing_copies_and_origins','c08_resource_library_pages_specs_origin_and_stale_requests','c08_resource_origin_link_focuses_exact_entry_and_returns_to_timeline','c08_cache_budget_clear_rebuild_preserves_sources_and_rejects_partial_search','c08_dates_specs_alias_tags_and_source_links_share_pagination','c08_quoted_ascii_cjk_trigrams_equal_literal_substrings','retains a still-matching selection that moved outside the loaded sort window',
            'retains loaded pages, selected variant, node and playback position across related updates',
            'switches only after the selected entry leaves the refreshed result and explains the change',
            'late refresh snapshots and details cannot overwrite a newer refresh or user selection',
            'A review: unrelated archive events preserve the selected entry and player',
            '组合筛选不修改原数组，排序只改变结果',
            '预览搜索、空结果和卡片详情同步',
            '示例订阅和下载不调用档案 API',
            '实际空档案没有虚构的数量、作者或已保存文件',
            '读取失败显示重试，恢复后清除错误',
            '真实记录显示实际状态，媒体操作不调用不安全路径 API',
            '侧栏是真实路由，后台状态入口保留',
            '晚到的旧请求不能覆盖新的搜索结果',
            '真实订阅失败保留持久状态，成功后重新读取并支持账号组合查询',
            '卸载后的详情请求不更新页面或发起新写操作',
            '真实侧栏只提供当前平台，预览平台不成为生产入口',
            '断连提示和重连快照事件实际刷新，卸载释放监听',
            'c08_rich_article_text_is_inert_and_preserves_inline_format',
            'c08_two_site_all_types_filters_pagination_current_revisions_and_ipc',
            'c08_document_resource_ui_races_and_live_controls'],
    'C09': ['c09_packaged_two_site_isolation_and_complete_inventory','c09_minimized_embedded_webview_close_reconnect_and_full_exit'],
    'C10': ['test_rejects_package_manifest_and_build_tampering',
             'test_rejects_stale_source_missing_failed_duplicate_cases',
             'test_rejects_missing_package_failed_short_benchmark_and_stale_docs'],
}

REQUIRED_CASES['C03'] += ['declared_douyin_media_length_reaches_candidate','media_lengths_cover_selected_variant_motion_music_and_invalid_claims']
REQUIRED_CASES['C06'] += ['douyin_auth_probe_requires_self_profile_not_guest_uid']
REQUIRED_CASES['C08'] += ['c08_live_quality_query_ignores_unrelated_recording_fragments']
REQUIRED_CASES['C11'] = [
 'overlapping_subscription_can_continue_same_entry_after_owner_pause',
 'c11_shared_observe_and_media_cover_all_owner_states_and_policies',
 'c11_shared_control_revocation_restart_and_cancel_do_not_spend_failures',
 'c11_shared_auth_failure_and_credential_change_do_not_cross_consumers',
 'c11_upgrade_accept_reject_keep_both_preserve_old_until_explicit_atomic_decision',
 'c11_upgrade_stale_confirmations_reject_selection_revision_generations_cancel_and_corruption',
 'c11_incomplete_or_cancelled_candidate_never_borrows_old_required_tracks',
 'c11_authentication_timelines_share_identity_counts_pages_and_immutable_history',
 'c11_same_blob_distinct_acquisitions_backup_restore_keep_history_without_credentials',
 'c11_two_production_auth_probes_require_batch_proof_and_preserve_expired_unknown',
 'c11_schema4_migration_unknown_history_and_failure_roll_back_without_rewriting_old_migrations',
 'c11_overlapping_production_subscriptions_keep_authenticated_cdn_history_and_old_selection',
 'c11_login_completion_is_bounded_persistent_deduplicated_and_excludes_live_deleted',
 'c11_identical_bytes_keep_two_authentication_histories_without_false_upgrade',
 'c11_pending_upgrade_survives_restart_and_anonymous_fallback_has_new_context',
 'c11_completed_candidate_cancel_and_failed_attempt_keep_old_resources_and_history',
 'c11_edited_completion_and_variant_labels_are_explicit_without_replacing_history',
 'c11_pending_candidates_never_become_default_playback_and_keep_resource_identity_labels',
 'c11_acquisition_history_is_lazy_paginated_and_does_not_relabel_old_results',
 'c11_legacy_recovery_cache_verification_does_not_invent_acquisition_identity',
 'c11_restore_invalidates_pending_confirmations_and_retry_finishes_original_acquisition',
 'c11_metadata_pacing_fifo_and_urgent_budget_prevent_consumer_starvation',
 'c11_upgrade_page_requires_explicit_selection_and_preserves_failed_comparison',
 'c11_home_authentication_filter_resets_cursor_and_preserves_media',
]
REQUIRED_CASES['C12'] = ['c12_fragment_authentication_actual_specs_gaps_restart_export_and_no_backfill',
 'c12_live_fragment_labels_keep_mixed_unknown_and_actual_bitrate']
REQUIRED_CASES['C13'] = ['test_missing_credentials_stop_before_any_output',
 'test_preparation_keeps_secret_out_of_output_and_requires_new_scope',
 'test_duplicate_and_malformed_inputs_are_rejected',
 'c13_metadata_403_412_stop_as_access_restrictions_without_empty_success_or_cookie_expiry',
 'test_r2_contract_requirements_and_configuration_are_required_inputs']

# Keep every r1/r2 required case; r3 is an independent extension.
REQUIRED_CASES['C14'] = [
 'a_completed_shared_consumer_keeps_its_downloaded_upgrade_available',
 'a_delete_restore_does_not_promote_a_rejected_upgrade_to_selection',
 'a_prepared_bytes_from_cancelled_job_are_not_ready_for_new_completion',
 'c14_completed_candidate_survives_successor_same_lower_failure_and_new_offer',
 'c14_shared_candidate_comparison_keeps_originating_quality_preference',
 'c14_restore_copies_archive_pending_rejected_pinned_missing_and_recycled',
 'c14_materialization_owner_checked_at_receiving_prepared_ready_and_committed',
 'c14_pending_retry_reuses_only_its_own_variant_and_legacy_token_shape',
 'c14_signed_url_uses_32_character_key_and_one_timestamp',
 'c14_secsdk_context_is_scoped_unique_and_never_returned',
 'c14_secsdk_wire_bytes_and_cross_origin_redirect_never_export_context',
 'c14_https_normalization_is_exact_and_limited_to_verified_cdn',
 'c14_production_live_links_resolve_alias_room_owner_and_explicit_streams',
 'c14_acquisition_budget_survives_restart_and_stops_on_denial',
 'c14_known_live_media_cdns_work_without_user_allowlist_or_cookies',
 'c14_production_post_page_survives_bounded_metadata_transport',
 'c14_large_messages_preserve_all_bytes_and_reject_corruption_order_and_budget',
 'c14_large_interleaving_expiry_restart_and_single_item_ceiling',
 'c14_source_preview_expiry_credential_change_idempotency_cancel_and_restart',
 'c14_live_distinct_previews_share_queued_request_and_reject_changed_choice',
 'c14_direct_live_offline_seals_but_denial_and_unknown_do_not',
 'c14_selection_rejects_downgrade_required_resource_changes_and_actual_mismatch',
 'c14_two_production_links_selected_media_core_objects_offline_and_upgrade',
 'c14_link_ui_requires_choice_keeps_audio_and_rejects_stale_double_submit',
 'c14_link_ui_ignores_late_preview_after_source_or_identity_change',
 'c14_link_ui_stops_core_recording_and_demo_has_zero_calls',
 'c14_link_ui_distinguishes_offline_denied_and_unsupported_previews',
 'c14_offline_source_errors_keep_safe_state_without_adapter_payload',
 'c14_public_wbi_keys_do_not_prove_login_or_bypass_challenges',
 'c14_dash_choice_survives_safe_id_normalization_and_keeps_required_audio',
 'test_r3_rejects_missing_diagnostic_stale_low_resolution_and_incomplete_real_chain',
 'test_r3_real_catalog_rejects_wrong_owner_unsealed_and_false_duration',
 'test_r3_real_prior_run_budget_is_cumulative',
 'c14_real_bilibili_video','c14_real_bilibili_live','c14_real_douyin_video','c14_real_douyin_live',
]


def requirement_coverage(root:Path,cases:list[dict])->list[dict]:
    mapping=read(root/'docs/delivery/requirements-map.json')['requirements']
    passed={c['name'] for c in cases if c['status']=='passed'}
    result=[]
    for row in mapping:
        scope=row.get('r3',{});applicable=scope.get('applicable',False)
        names=scope.get('testCases',[]);implementation=scope.get('implementation',[])
        status='passed' if applicable and names and set(names)<=passed and implementation else 'not-run'
        result.append({'id':row['id'],'applicable':applicable,'status':status,
                       'testCases':names if applicable else [],'implementation':implementation if applicable else []})
    return result


def validate_contract(bundle:dict,root:Path,cases:list[dict],expected:dict)->list[str]:
    errors=[]
    plan=(root/'docs/delivery/stage-plan.md').read_text(encoding='utf8')
    schema=(root/'crates/domain/src/lib.rs').read_text(encoding='utf8')
    if PLAN_ID not in plan or not re.search(r'SCHEMA_VERSION:\s*i64\s*=\s*'+str(SCHEMA_VERSION)+r'\s*;',schema):errors.append('execution plan or schema differs from evidence contract')
    if bundle.get('schemaVersion')!=SCHEMA_VERSION:errors.append('wrong evidence schema version')
    coverage=requirement_coverage(root,cases)
    if len(coverage)!=87 or sum(r['applicable'] for r in coverage)!=68 or len({r['id'] for r in coverage})!=87:errors.append('incomplete 87/68 requirement scope')
    for row in read(root/'docs/delivery/requirements-map.json')['requirements']:
        current=row.get('r3',{})
        if current.get('plan')!=PLAN_ID:errors.append('missing independent r3 mapping')
        if current.get('baselineMapping')=='r2' and (not row.get('r2') or not set(row['r2'].get('testCases',[]))<=set(current.get('testCases',[]))):errors.append('original r2 case mapping was lost')
    if bundle.get('requirementCoverage')!=coverage:errors.append('requirements not bound to current mapping and cases')
    for row in coverage:
        if row['applicable'] and (row['status']!='passed' or any(p not in expected for p in row['implementation'])):errors.append('unproved M1 requirement '+row['id'])
        if not row['applicable'] and row['status']!='not-run':errors.append('deferred requirement claimed complete')
    return errors


def sha(path:Path)->str:
    h=hashlib.sha256()
    with path.open('rb') as f:
        for block in iter(lambda:f.read(1<<20),b''):h.update(block)
    return h.hexdigest()

def inputs(root:Path=ROOT)->dict[str,str]:
    names=subprocess.check_output(['git','ls-files','-z','--cached','--others','--exclude-standard'],cwd=root).decode().split('\0')
    prefixes=('crates/','adapters/','apps/','tests/','scripts/','.cargo/')
    roots={'Cargo.toml','Cargo.lock','rust-toolchain.toml','package.json','pnpm-lock.yaml','pnpm-workspace.yaml','media-backend.lock.json','.env.example','docs/delivery/stage-plan.md','docs/site-archive-contracts.md','docs/requirements.md','docs/delivery/requirements-map.json','LICENSE'}
    return {n:sha(root/n) for n in sorted(set(names)) if (n.startswith(prefixes) or n in roots) and (root/n).is_file()}

def documents(root:Path=ROOT)->dict[str,str]:
    return {p.relative_to(root).as_posix():sha(p) for p in sorted([*(root/'docs').rglob('*'),*root.glob('*.md')]) if p.is_file()}

def contained(root:Path,name:str)->Path:
    """Evidence never follows absolute paths, parent traversal, links or junctions."""
    if not isinstance(name,str) or not name or '\\' in name or ':' in name:
        raise ValueError('invalid evidence path')
    path=Path(name)
    if path.is_absolute() or any(part in ('..','.','') for part in name.split('/')) or not path.parts:
        raise ValueError('evidence path outside workspace')
    current=root.resolve()
    for part in path.parts:
        current=current/part
        if current.is_symlink() or (hasattr(current,'is_junction') and current.is_junction()):
            raise ValueError('linked evidence path')
    if not current.resolve().is_relative_to(root.resolve()):raise ValueError('evidence path escapes workspace')
    return current

def inventory(folder:Path)->dict[str,str]:
    result={}
    for p in sorted(folder.rglob('*')):
        checked=contained(folder,p.relative_to(folder).as_posix())
        if checked.is_file():result[p.relative_to(folder).as_posix()]=sha(checked)
    return result

def read(path:Path)->dict:
    return json.loads(path.read_text(encoding='utf8'))

def validate_package(folder:Path,expected:dict,root:Path=ROOT)->list[str]:
    """Bind the package's own manifest, executable inventory and build receipts."""
    try:
        actual=inventory(folder);manifest=read(folder/'manifest.json');errors=[]
        sums={}
        for line in (folder/'SHA256SUMS.txt').read_text(encoding='utf8').splitlines():
            digest,name=line.split('  ',1);contained(folder,name)
            if name in sums:errors.append('duplicate package checksum')
            sums[name]=digest
        if sums!={n:h for n,h in actual.items() if n!='SHA256SUMS.txt'}:errors.append('package checksums do not cover actual files')
        components={'trace-desktop.exe','trace-daemon.exe','adapters/trace-adapter-bilibili.exe','adapters/trace-adapter-douyin.exe'}
        if set(manifest['components'])!=components or len(manifest['components'])!=4:errors.append('incomplete package components')
        if manifest['built_binaries']!={n:actual[n] for n in components}:errors.append('package executable build hashes differ')
        if {n for n in actual if n.endswith('.exe')}!=components|{'media/ffmpeg.exe','media/ffprobe.exe','media/ffplay.exe'}:errors.append('extra or missing package executable')
        if manifest['sourceInputs']!=expected or manifest['source_files_sha256']!=expected:errors.append('package manifest source mismatch')
        if manifest['source_tree_sha256']!=hashlib.sha256(json.dumps(expected,sort_keys=True,separators=(',',':')).encode()).hexdigest():errors.append('package aggregate source mismatch')
        if manifest['stage']!=PLAN_ID or manifest['schema_version']!=SCHEMA_VERSION or manifest['productAcceptance']!='not-run':errors.append('package stage or acceptance mismatch')
        commands=manifest['build_commands']
        if {Path(c['argv'][0]).stem for c in commands}!={'cargo','corepack'}:errors.append('missing package compiler commands')
        if not any('trace-desktop/custom-protocol' in c['argv'] for c in commands):errors.append('missing embedded production frontend')
        for command in commands:
            if command['exitCode']!=0 or command['sourceInputs']!=expected or command['sourceUnchanged'] is not True:errors.append('failed or stale package build command')
            if sha(contained(root,command['log']))!=command['logSha256']:errors.append('missing or changed package build log')
        profile=read(folder/'profile.example.json')
        if profile['test_allowed_hosts'] or profile.get('extra_media_hosts') or profile.get('allow_media_path'):errors.append('package contains development network configuration')
        media=read(folder/'media/verified.json')
        if media['lock']!=read(root/'media-backend.lock.json'):errors.append('package media lock differs')
        for name,digest in media['files'].items():
            if sha(contained(folder,'media/'+name))!=digest:errors.append('package media file changed')
        if not {'ffmpeg.exe','ffprobe.exe','ffplay.exe'}<=set(media['files']):errors.append('missing verified media executables')
        for name in ['LICENSE','adapters/douyin-NOTICE.txt','dependency-licenses.md','media/LICENSE.txt','media/SOURCE.txt']:
            if name not in actual:errors.append('missing package legal material')
        if (root/'LICENSE').is_file() and actual.get('LICENSE')!=sha(root/'LICENSE'):errors.append('package project license mismatch')
        return errors
    except (AttributeError,KeyError,TypeError,ValueError,OSError):return ['invalid package manifest or build evidence']

def validate(bundle:dict,root:Path=ROOT,expected:dict|None=None,docs:dict|None=None,required:dict|None=None)->list[str]:
    try:return validate_contents(bundle,root,expected,docs,required)
    except (AttributeError,KeyError,TypeError,IndexError,ValueError,OSError):return ['invalid evidence schema']


def validate_contents(bundle:dict,root:Path=ROOT,expected:dict|None=None,docs:dict|None=None,required:dict|None=None)->list[str]:
    errors=[];expected=inputs(root) if expected is None else expected
    docs=documents(root) if docs is None else docs;required=REQUIRED_CASES if required is None else required
    if bundle.get('plan')!=PLAN_ID:errors.append('wrong plan')
    if bundle.get('sourceInputs')!=expected:errors.append('stale or incomplete source fingerprint')
    if bundle.get('documents')!=docs:errors.append('stale or incomplete documents')
    if bundle.get('productAcceptance')!='not-run':errors.append('automatic evidence cannot declare product acceptance')
    cases=bundle.get('cases',[]);by_name={}
    errors.extend(validate_contract(bundle,root,cases,expected))
    for case in cases:
        name=case.get('name');by_name.setdefault(name,[]).append(case)
        if case.get('status')!='passed':errors.append(f'failed or skipped case: {name}')
    for group,names in required.items():
        for name in names:
            found=by_name.get(name,[])
            if len(found)!=1:errors.append(f'{group}: missing or duplicate required case {name}')
    identities=[c.get('id',c.get('name')) for c in cases]
    if len(set(identities))!=len(identities):errors.append('duplicate case identifiers')
    receipts=bundle.get('commands',[])
    if not receipts:errors.append('missing command receipts')
    if {r.get('kind') for r in receipts}!={'test','bench','package','report'}:errors.append('missing evidence command kind')
    if len({r.get('id') for r in receipts})!=len(receipts):errors.append('duplicate command identifiers')
    recorded_cases=[case for r in receipts if r.get('kind')=='test' for case in r.get('cases',[])]
    if cases!=recorded_cases:errors.append('case list not bound to command receipts')
    for r in receipts:
        if r.get('exitCode')!=0:errors.append(f'failed command {r.get("id")}')
        if r.get('sourceUnchanged') is not True:errors.append(f'command inputs changed {r.get("id")}')
        if r.get('sourceInputs')!=expected:errors.append(f'stale command {r.get("id")}')
        try:
            p=contained(root,r.get('log',''))
            if not p.is_file() or sha(p)!=r.get('logSha256'):errors.append(f'missing or altered raw log {r.get("id")}')
            elif r.get('kind')=='test' and parse_cases(p.read_text(encoding='utf8',errors='replace'))!=r.get('cases'):errors.append(f'cases do not match raw log {r.get("id")}')
        except (ValueError,OSError):errors.append(f'invalid raw log path {r.get("id")}')
    package=bundle.get('package',{});declared=package.get('files',{})
    try:
        folder=contained(root,package.get('directory',''))
        actual=inventory(folder) if folder.is_dir() else {}
    except (ValueError,OSError):actual={};errors.append('invalid package path')
    if not declared or declared!=actual:errors.append('missing, extra or changed package member')
    for name in ['trace-desktop.exe','trace-daemon.exe','adapters/trace-adapter-bilibili.exe','adapters/trace-adapter-douyin.exe','adapters/douyin-NOTICE.txt','media/ffmpeg.exe','media/ffprobe.exe']:
        if name not in actual:errors.append(f'package missing {name}')
    if package.get('sourceInputs')!=expected:errors.append('stale package inputs')
    if actual:errors.extend(validate_package(folder,expected,root))
    bench=bundle.get('benchmark',{})
    if bench.get('exitCode')!=0 or bench.get('status')!='passed':errors.append('failed or absent benchmark')
    if bench.get('sourceInputs')!=expected or bench.get('sourceUnchanged') is not True:errors.append('stale benchmark inputs')
    minimum={'durationSeconds':3600,'metadataEntries':100000,'mediaMetadata':500000,'bodyMedianChars':500,'ordinaryCheckTargets':100,'liveCheckTargets':20,'httpLatencyMs':100,'queryPageSize':50,'streamBytesPerSecond':1000000,'parallelDownloads':2,'parallelRecordings':2,'recordingOnlyStreams':4,'mixedParallelSamples':1}
    for key,value in minimum.items():
        if not isinstance(bench.get(key),(int,float)) or bench[key]<value:errors.append(f'benchmark missing/short {key}')
    if set(bench.get('guiModes',[]))!={'open','closed','reconnected'}:errors.append('missing GUI lifecycle samples')
    for metric in ['privateWorkingSet','cpuAllProcesses','handles','queryP95Ms','queryP99Ms','throughputBytes','queueDepth','freeDiskBytes']:
        value=bench.get('measurements',{}).get(metric)
        if not isinstance(value,(int,float)) or isinstance(value,bool) or not math.isfinite(value) or value<0:errors.append(f'missing raw benchmark metric {metric}')
    try:raw=contained(root,bench.get('samples',''))
    except (ValueError,OSError):raw=root/'__missing__';errors.append('invalid samples path')
    if not raw.is_file() or sha(raw)!=bench.get('samplesSha256'):errors.append('missing or changed benchmark samples')
    else:
        try:
            raw_data=read(raw);samples=raw_data['samples'];times=[s['elapsedSeconds'] for s in samples]
            if raw_data.get('workload',{}).get('queryCacheState')!='new_core_connection_os_cache_unflushed':errors.append('missing cold-start connection evidence')
            recovery=raw_data.get('lifecycle',{}).get('recovery',{})
            if recovery.get('unfinishedOrdinaryTasks',0)<100 or recovery.get('serviceAccessible') is not True:errors.append('missing recovery workload and service evidence')
            derived=derive(raw_data)
            if any(bench.get(key)!=value for key,value in derived.items()):errors.append('benchmark summary does not match raw counters')
            if raw_data.get('sourceInputs')!=expected:errors.append('stale raw benchmark inputs')
            if raw_data.get('guiExecutableSha256')!=declared.get('trace-desktop.exe'):errors.append('benchmark GUI is not the packaged executable')
            probes=raw_data.get('lifecycle',{}).get('guiProbe',[])
            if len(probes)!=2 or any(p.get('embeddedPage') is not True or p.get('lifecycleControls') is not True or p.get('schemaVersion')!=SCHEMA_VERSION or p.get('r2AcquisitionQueries') is not True or p.get('r3SourceQueries') is not True for p in probes) or len({p.get('instanceId') for p in probes})!=1:errors.append('missing packaged GUI page and reconnect verification')
            build=raw_data['build'];build_log=contained(root,build['log'])
            if build.get('exitCode')!=0 or sha(build_log)!=build['logSha256']:errors.append('invalid benchmark build receipt')
            if set(build.get('binaries',{}))!={'m1_resource_bench.exe','m1_bench_adapter.exe'}:errors.append('incomplete benchmark executable inventory')
            for name,digest in build.get('binaries',{}).items():
                binary=contained(root,'target/measure/'+name)
                if not binary.is_file() or sha(binary)!=digest:errors.append('benchmark executable changed after build')
            if derived['idleWindowSeconds']<900 or derived['recordingStarts']<4 or derived['jobErrors']!=0:errors.append('incomplete workload or lifecycle samples')
            if len(times)<721 or times[-1]-times[0]<3600 or any(b<=a or b-a>15 for a,b in zip(times,times[1:])):errors.append('insufficient continuous benchmark sample window')
            for sample in samples:
                if not sample.get('processes') or any(not isinstance(p.get('privateWorkingSet'),int) or p['privateWorkingSet']<0 for p in sample['processes']):errors.append('incomplete process resource sample');break
        except (ValueError,KeyError,TypeError,IndexError,OSError):errors.append('invalid benchmark sample schema')
    targets={'idleCpuMeanPct':1,'idleMemoryP95MiB':150,'hotQueryP95Ms':500,'coldQueryP95Ms':1500,'recordingOnlyCpuMeanPct':5,'recordingOnlyMemoryP95MiB':300,'startRecordingP95Seconds':10,'recoverySeconds':30,'guiCloseSeconds':10}
    for metric,maximum in targets.items():
        value=bench.get('measurements',{}).get(metric)
        if not isinstance(value,(int,float)) or not math.isfinite(value) or value<0 or value>maximum:errors.append(f'unproved or failed NFR target {metric}')
    from real_chain_gate import validate_real
    errors.extend(validate_real(bundle.get('realSource',[]),root,expected,package))
    return errors

def parse_cases(output:str)->list[dict]:
    clean=re.sub(r'\x1b\[[0-9;]*m','',output)
    cases=[];suite='unknown'
    for line in clean.splitlines():
        running=re.search(r'Running .*\((?:.*[/\\])?([^/\\]+?)-[0-9a-f]+(?:\.exe)?\)',line)
        if running:suite=running[1]
        case=re.match(r'^test (\S+) \.\.\. (ok|FAILED|ignored)',line)
        if case:
            cases.append({'id':f'rust:{suite}:{case[1]}','name':case[1].split('::')[-1],'status':'passed' if case[2]=='ok' else 'failed'})
            continue
        case=re.match(r'^((?:test_|c[0-9]{2}_)\w+) \(([^)]+)\) \.\.\. (ok|FAIL|ERROR|skipped)',line)
        if case:
            cases.append({'id':f'python:{case[2]}','name':case[1],'status':'passed' if case[3]=='ok' else 'failed'})
            continue
        case=re.match(r'^\s*([✓✔×✗])\s+(.+?) > (.+?)\s+\d+(?:\.\d+)?ms\s*$',line)
        if case:
            # Vitest includes every describe() group after the file name. Match
            # required leaf names while preserving the full scoped identity.
            cases.append({'id':f'vitest:{case[2]}:{case[3]}','name':case[3].rsplit(' > ',1)[-1].strip(),'status':'passed' if case[1] in ('✓','✔') else 'failed'})
    return cases

def run_receipt(args)->int:
    before=inputs();out=Path(args.output).resolve();out.parent.mkdir(parents=True,exist_ok=True)
    log=out.with_suffix('.log');start=time.time()
    if out.exists() or log.exists():raise ValueError('choose a new receipt path; prior failures are preserved')
    with log.open('w',encoding='utf8') as f:
        env=dict(os.environ,NO_COLOR='1',FORCE_COLOR='0',PYTHONIOENCODING='utf-8')
        p=subprocess.run(args.command,cwd=ROOT,stdout=f,stderr=subprocess.STDOUT,text=True,encoding='utf8',env=env)
    text=log.read_text(encoding='utf8',errors='replace')
    cases=parse_cases(text)
    data={'id':args.id,'kind':args.kind,'argv':args.command,'startedUnix':start,'durationSeconds':time.time()-start,'exitCode':p.returncode,'sourceInputs':before,'sourceUnchanged':before==inputs(),'cases':cases,'log':log.relative_to(ROOT).as_posix(),'logSha256':sha(log)}
    out.write_text(json.dumps(data,ensure_ascii=False,indent=2)+'\n',encoding='utf8')
    return p.returncode if data['sourceUnchanged'] else 1

def assemble(args)->int:
    receipts=[read(Path(path)) for path in args.receipts]
    folder=Path(args.package).resolve();manifest=read(folder/'manifest.json')
    benchmark=read(Path(args.benchmark))
    bundle={'plan':PLAN_ID,'schemaVersion':SCHEMA_VERSION,'requirementCoverage':requirement_coverage(ROOT,[c for r in receipts if r.get('kind')=='test' for c in r.get('cases',[])]),'sourceInputs':inputs(),'documents':documents(),'productAcceptance':'not-run',
        'commands':receipts,'cases':[c for r in receipts if r.get('kind')=='test' for c in r.get('cases',[])],
        'package':{'directory':folder.relative_to(ROOT).as_posix(),'sourceInputs':manifest.get('sourceInputs'),
            'files':{p.relative_to(folder).as_posix():sha(p) for p in sorted(folder.rglob('*')) if p.is_file()}},'benchmark':benchmark,
        'realSource':[{'path':Path(path).resolve().relative_to(ROOT).as_posix(),'sha256':sha(Path(path))} for path in args.real_source]}
    errors=validate(bundle)
    bundle['status']='failed' if errors else 'passed';bundle['errors']=errors
    out=Path(args.output);out.parent.mkdir(parents=True,exist_ok=True);out.write_text(json.dumps(bundle,ensure_ascii=False,indent=2)+'\n',encoding='utf8')
    for error in errors:print('[FAIL]',error)
    return int(bool(errors))

def main()->int:
    parser=argparse.ArgumentParser();sub=parser.add_subparsers(dest='action',required=True)
    run=sub.add_parser('run');run.add_argument('--kind',required=True,choices=['test','bench','package','report']);run.add_argument('--id',required=True);run.add_argument('--output',required=True);run.add_argument('command',nargs=argparse.REMAINDER)
    check=sub.add_parser('check');check.add_argument('bundle')
    build=sub.add_parser('assemble');build.add_argument('--receipts',nargs='+',required=True);build.add_argument('--package',required=True);build.add_argument('--benchmark',required=True);build.add_argument('--output',required=True);build.add_argument('--real-source',nargs='+',required=True)
    args=parser.parse_args()
    if args.action=='assemble':return assemble(args)
    if args.action=='run':
        if args.command[:1]==['--']:args.command=args.command[1:]
        if not args.command:parser.error('missing command')
        return run_receipt(args)
    errors=validate(read(Path(args.bundle)))
    for e in errors:print('[FAIL]',e)
    if errors:return 1
    print('B automated evidence passed; A review pending; product acceptance not-run.');return 0

if __name__=='__main__':sys.exit(main())
