<script setup lang="ts">
import { onBeforeUnmount, onMounted, ref } from "vue";
import { NAlert, NButton, NCard, NDescriptions, NDescriptionsItem, NTag, useMessage } from "naive-ui";
import { api, type HealthIssue, type DaemonStatus } from "../lib/api";
import { formatBytes, formatTime, jobStateLabel } from "../lib/format";

const message = useMessage();
const status = ref<DaemonStatus | null>(null);
const error = ref<string | null>(null);
const busy = ref(false);
const closing = ref(false);
async function closeDesktop(exit=false) {
  if(closing.value)return;
  closing.value=true;
  try { if(exit)await api.desktopExit();else await api.desktopClose(); }
  catch(e){message.error(String(e));closing.value=false;}
}
const startup=ref<{enabled:boolean;current_location:boolean}|null>(null),startupError=ref('');
const issues=ref<HealthIssue[]>([]),selectedIssue=ref<string|null>(null),refs=ref<{job_id:string;subscription_id?:string;active:boolean;state?:string}[]>([]),nextRef=ref<string|null>(null);
async function loadStartup(){try{startup.value=await api.startupStatus();startupError.value='';}catch{startupError.value='启动设置暂不可用，请确认 Profile 已保存。';}}
async function toggleStartup(){if(!startup.value)return;try{startup.value=await api.startupConfigure(!startup.value.enabled);}catch(e){message.error(String(e));}}
async function inspectIssue(key:string,more=false){if(!more){selectedIssue.value=key;refs.value=[];nextRef.value=null;}try{const page=await api.healthRefs(key,more?nextRef.value??undefined:undefined);if(selectedIssue.value!==key)return;refs.value=more?[...refs.value,...page.items]:page.items;nextRef.value=page.next??null;}catch(e){message.error(String(e));}}
let timer: number | undefined;

async function refresh() {
  if (closing.value) return;
  try {
    status.value = await api.daemonStatus();
    issues.value=(await api.healthList()).items;
    error.value = null;
  } catch (e) {
    error.value = String(e);
  }
}

async function togglePause() {
  if (!status.value) return;
  busy.value = true;
  try {
    status.value = await api.daemonPause(!status.value.paused);
  } catch (e) {
    message.error(String(e));
  } finally {
    busy.value = false;
  }
}

onMounted(() => {
  refresh();
  loadStartup();
  timer = window.setInterval(refresh, 5000);
});
onBeforeUnmount(() => window.clearInterval(timer));
</script>

<template>
  <n-card title="后台状态">
    <template #header-extra>
      <n-button size="small" @click="refresh">刷新</n-button>
    </template>
    <n-alert v-if="error" type="error" title="无法连接独立后台">
      {{ error }}
      <p>请确认后台正在运行。连接中断后会等待后台恢复；如已完全退出，请关闭并重新打开界面。</p>
    </n-alert>
    <n-descriptions v-else-if="status" bordered :column="2" size="small">
      <n-descriptions-item label="档案">{{ status.archive_id }}</n-descriptions-item>
      <n-descriptions-item label="实例">{{ status.instance_id }}</n-descriptions-item>
      <n-descriptions-item label="运行状态">
        <n-tag :type="status.paused ? 'warning' : 'success'">
          {{ status.paused ? "已暂停调度" : "调度中" }}
        </n-tag>
      </n-descriptions-item>
      <n-descriptions-item label="启动时间">{{ formatTime(status.started_at) }}</n-descriptions-item>
      <n-descriptions-item label="任务统计">
        <span v-for="(v, k) in status.job_counts" :key="k" class="count-item">
          {{ jobStateLabel(String(k)) }} {{ v }}
        </span>
        <span v-if="!Object.keys(status.job_counts).length">无任务</span>
      </n-descriptions-item>
      <n-descriptions-item v-if="status.budgets" label="当前采集预算">
        元数据最小间隔 {{status.budgets.metadata_min_interval_ms}} ms，加 0～{{status.budgets.metadata_jitter_ms}} ms 抖动；每轮最多 {{status.budgets.scan_page_budget}} 页；录制并发 {{status.budgets.live_concurrency}}；周期抖动 ±{{status.budgets.check_jitter_percent}}%。可在 Profile 中调整。
      </n-descriptions-item>
      <n-descriptions-item label="媒体后端">
        {{ status.media_backend?.version ?? "未检测到 ffmpeg/ffprobe（外部打开仍可用）" }}
      </n-descriptions-item>
    </n-descriptions>
    <p v-if="startupError">{{startupError}}</p>
    <p v-if="startup"><n-button size="small" @click="toggleStartup">{{startup.enabled?'关闭登录后启动':'启用登录后启动'}}</n-button> 登录 Windows 后只启动后台；关闭界面仍继续采集。<span v-if="startup.enabled&&!startup.current_location">程序位置已变化，请关闭后重新启用。</span></p>
    <section aria-label="采集告警"><h3>采集告警</h3><p v-if="!issues.length">暂无已记录的问题。</p>
      <article v-for="issue in issues" :key="issue.issue_key"><strong>{{issue.category}} · {{issue.resolved_at?'已恢复':'待处理'}}</strong><p>{{issue.detail.platform}} · 影响 {{issue.affected_jobs}} 项任务 · {{formatTime(issue.last_seen_at)}}</p><p v-if="issue.notification_state==='failed'">系统通知未能提交；问题仍保留于此。</p><n-button size="small" @click="inspectIssue(issue.issue_key)">查看订阅和任务</n-button></article>
      <div v-if="selectedIssue"><ul><li v-for="item in refs" :key="item.job_id">订阅 {{item.subscription_id??'本地任务'}} · {{item.job_id}} · {{item.state}} <n-button size="tiny" @click="api.jobRetry(item.job_id).then(refresh).catch(e=>message.error(String(e)))">重试</n-button></li></ul><n-button v-if="nextRef" size="small" @click="inspectIssue(selectedIssue!,true)">更多受影响任务</n-button><p><router-link to="/accounts">账号与登录</router-link> · <router-link to="/tasks">任务</router-link> · <router-link to="/storage">存储</router-link></p></div>
    </section>
    <template #footer>
      <n-button size="small" :disabled="!status" :loading="busy" @click="togglePause">
        {{ status?.paused ? "恢复调度" : "暂停调度" }}
      </n-button>
      <n-button data-action="desktop-close" size="small" :disabled="closing" @click="closeDesktop()">关闭界面继续后台</n-button>
      <n-button data-action="desktop-exit" size="small" type="warning" :disabled="!status" :loading="closing" @click="closeDesktop(true)">完全退出</n-button>
      <p>{{closing?'正在等待后台结束工作…':'关闭界面会释放窗口资源并继续采集；完全退出会停止调度、封口录制并结束后台。'}}</p>
      <span class="hint">
        对象存储：{{ formatBytes(Number(status?.storage?.objects_bytes ?? NaN)) }}
      </span>
    </template>
  </n-card>
</template>

<style scoped>
.count-item {
  margin-right: 12px;
}
.hint {
  margin-left: 12px;
  color: var(--n-text-color-3, #888);
  font-size: 12px;
}
</style>
