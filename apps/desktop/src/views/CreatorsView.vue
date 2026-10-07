<script setup lang="ts">
import { onMounted, ref,watch } from "vue";
import AccountHistory from '../components/AccountHistory.vue';
import {
  NButton,
  NCard,
  NDataTable,
  NDivider,
  NForm,
  NFormItem,
  NInput,
  NInputNumber,
  NModal,
  NSelect,
  NTabPane,
  NTabs,
  NTag,
  useMessage,
  type DataTableColumns,
} from "naive-ui";
import { h } from "vue";
import {
  api,
  type AccountDto,
  type CreatorDto,
  type SubscriptionDto,
  type QualityRequirement,
} from "../lib/api";
import { platformOf } from "../lib/format";

const message = useMessage();
const creators = ref<CreatorDto[]>([]);
const loading = ref(false);

// 新建创作者
const showCreate = ref(false);
const newDisplayName = ref("");
const newNotes = ref("");
const editCreator=ref<CreatorDto|null>(null),editName=ref(''),editNotes=ref(''),editAliases=ref(''),editTags=ref('');
function openEdit(c:CreatorDto){editCreator.value=c;editName.value=c.display_name;editNotes.value=c.notes??'';editAliases.value=(c.aliases??[]).join('，');editTags.value=(c.tags??[]).join('，');}
const splitLabels=(v:string)=>[...new Set(v.split(/[,，\n]/).map(s=>s.trim()).filter(Boolean))];
async function saveCreator(){if(!editCreator.value||!editName.value.trim())return;try{await api.creatorUpdate(editCreator.value.creator_id,editName.value.trim(),editNotes.value,{aliases:splitLabels(editAliases.value),tags:splitLabels(editTags.value)});editCreator.value=null;await refresh();}catch(e){message.error(String(e));}}
async function toggleCreator(c:CreatorDto){try{await api.creatorUpdate(c.creator_id,undefined,undefined,{status:c.status==='archived'?'active':'archived'});await refresh();}catch(e){message.error(String(e));}}

// 绑定平台账号（针对选定创作者）
const bindCreator = ref<CreatorDto | null>(null);
const bindNamespace = ref("bilibili");
const bindKey = ref("");
const bindCredential=ref<string|null>(null),bindCredentials=ref<{label:string;value:string}[]>([]),binding=ref(false);
watch(bindNamespace,async(namespace)=>{bindCredential.value=null;const rows=await api.credentialList(namespace).catch(()=>({items:[]}));if(namespace===bindNamespace.value)bindCredentials.value=rows.items.filter(c=>c.status==='valid').map(c=>({label:c.subject||c.credential_id,value:c.credential_id}));},{immediate:true});

// 订阅（针对选定账号）
const subAccount = ref<AccountDto | null>(null);
const editingSub=ref<SubscriptionDto|null>(null);
const subRangeKind = ref<"from_now" | "recent_days" | "all" | "date_range">("from_now");
const subStart=ref(''),subEnd=ref(''),subHeight=ref<number|null>(null),subFps=ref<number|null>(null),subStrict=ref(false),subPreferFps=ref(false),savingSub=ref(false);
function requestedQuality():QualityRequirement{return {min_height:subHeight.value,min_fps:subFps.value,strict:subStrict.value,prefer_fps:subPreferFps.value};}
function utcDate(v:string):string|undefined{return v?new Date(v).toISOString():undefined;}
function localDate(v:unknown):string{if(typeof v!=='string')return '';const d=new Date(v);if(!Number.isFinite(d.valueOf()))return '';return new Date(d.valueOf()-d.getTimezoneOffset()*60000).toISOString().slice(0,16);}
const subDays = ref(30);
const subIntervalSeconds = ref(300);
const subLiveIntervalSeconds=ref(30);
const subTypes = ref<string[]>(['video']);
const subSubtitles=ref(false);
const subChat=ref(false),subCredential=ref<string|null>(null),subCredentials=ref<{label:string;value:string}[]>([]);
const contentTypes = [{label:'视频',value:'video'},{label:'动态',value:'dynamic'},{label:'图文',value:'image'},{label:'专栏 / 长文',value:'article'},{label:'直播录制',value:'live'}];

const namespaces = [
  { label: "哔哩哔哩（bilibili）", value: "bilibili" },
  { label: "抖音（douyin）", value: "douyin" },
];

async function refresh() {
  loading.value = true;
  try {
    creators.value = (await api.creatorList()).items;
  } catch (e) {
    message.error(String(e));
  } finally {
    loading.value = false;
  }
}

async function create() {
  if (!newDisplayName.value.trim()) {
    message.warning("请填写展示名称");
    return;
  }
  try {
    await api.creatorCreate(newDisplayName.value.trim(), newNotes.value || undefined);
    message.success("已创建创作者");
    showCreate.value = false;
    newDisplayName.value = "";
    newNotes.value = "";
    refresh();
  } catch (e) {
    message.error(String(e));
  }
}

async function openBind(c: CreatorDto) {
  bindCreator.value = c;
  bindKey.value = "";
}

async function bind() {
  if (!bindCreator.value || !bindKey.value.trim()) {
    message.warning("请填写平台账号 ID（如 mid）");
    return;
  }
  try {
    binding.value=true;
    const creator=bindCreator.value.creator_id,namespace=bindNamespace.value;
    const source=await api.accountResolve(namespace,bindKey.value.trim(),bindCredential.value??undefined);
    await api.accountBind(creator, namespace, source.native_account_key);
    message.success("已绑定平台账号");
    bindCreator.value = null;
    refresh();
  } catch (e) {
    message.error(String(e));
  }
    binding.value=false;
}

async function openSubscribe(a: AccountDto, existing?:SubscriptionDto) {
  editingSub.value=existing??null;
  const range=typeof existing?.range==='string'?JSON.parse(existing.range):existing?.range as {kind?:string;days?:number;start?:string;end?:string}|undefined;
  subRangeKind.value=(range?.kind??'from_now') as typeof subRangeKind.value;subDays.value=range?.days??30;
  subStart.value=localDate(range?.start);subEnd.value=localDate(range?.end);
  subTypes.value=existing?[existing.content_type]:['video'];subIntervalSeconds.value=existing?.check_interval_seconds??300;subLiveIntervalSeconds.value=existing?.check_interval_seconds??30;
  subHeight.value=existing?.quality?.min_height??null;subFps.value=existing?.quality?.min_fps??null;subStrict.value=existing?.quality?.strict??false;subPreferFps.value=existing?.quality?.prefer_fps??false;
  subAccount.value = a;
  subCredential.value=existing?.credential_id??null;subChat.value=existing?.capture_chat??false;subSubtitles.value=existing?.capture_subtitles??false;subCredentials.value=[];
  const rows=await api.credentialList(a.namespace.split(':')[0]).catch(()=>({items:[]}));
  if(subAccount.value?.account_id===a.account_id)subCredentials.value=rows.items.filter(c=>c.status==='valid'||c.credential_id===existing?.credential_id).map(c=>({label:(c.subject||c.credential_id)+(c.status==='valid'?'':'（需验证）'),value:c.credential_id}));
}

async function subscribe() {
  if (!subAccount.value || savingSub.value) return;
  if(subStrict.value && !subHeight.value && !subFps.value){message.warning('严格模式需要最低高度或帧率。');return;}
  if(!subTypes.value.length){message.warning('请至少选择一种内容。');return;}
  savingSub.value=true;
  try {
    const start=utcDate(subStart.value),end=utcDate(subEnd.value);
    if(subRangeKind.value==='date_range' && (!start&&!end || start&&end&&start>=end)){message.warning('请填写有效的起止范围，至少一个边界。');return;}
    if(editingSub.value){
      const range=subRangeKind.value==='recent_days'?{kind:'recent_days',days:subDays.value}:subRangeKind.value==='date_range'?{kind:'date_range',start,end}:{kind:subRangeKind.value};
      const r=editingSub.value;
      await api.subscriptionUpdate(r.subscription_id,{range,quality:requestedQuality(),...(subCredential.value!==r.credential_id?{credential_id:subCredential.value??''}:{}),check_interval_seconds:r.content_type==='live'?subLiveIntervalSeconds.value:subIntervalSeconds.value,...(r.content_type==='live'?{capture_chat:subChat.value,capture_subtitles:subSubtitles.value}:{})});
    }else{
      for (const contentType of [...subTypes.value]) {
        await api.subscriptionCreate(subAccount.value.account_id,subRangeKind.value,subRangeKind.value==='recent_days'?subDays.value:undefined,contentType==='live'?subLiveIntervalSeconds.value:subIntervalSeconds.value,contentType,{credentialId:subCredential.value??undefined,captureChat:contentType==='live'&&subChat.value,captureSubtitles:contentType==='live'&&subSubtitles.value,quality:requestedQuality(),start,end});
        subTypes.value=subTypes.value.filter(t=>t!==contentType);
      }
    }
    message.success('订阅配置已保存。');subAccount.value=null;editingSub.value=null;await refresh();await loadSubs();
  } catch (e) { message.error(String(e)); } finally {savingSub.value=false;}
}
async function editSubscription(s:SubscriptionDto){const a=accounts.value.find(a=>a.account_id===s.account_id);if(a)await openSubscribe(a,s);}
async function toggleSubscription(s:SubscriptionDto){try{await api.subscriptionSetEnabled(s.subscription_id,s.state!=='enabled');await loadSubs();}catch(e){message.error(String(e));}}

async function checkNow(subscriptionId: string) {
  try {
    await api.subscriptionCheckNow(subscriptionId);
    message.success("已请求立即检查");
  } catch (e) {
    message.error(String(e));
  }
}

const creatorColumns: DataTableColumns<CreatorDto> = [
  { title: "创作者", key: "display_name" },
  { title: "状态", key: "status", render:r=>r.status==='archived'?'已归档':'活跃' },
  {
    title: "操作",
    key: "actions",
    width: 360,
    render(row) {
      return h("div", { class: "row-actions" }, [
        h(NButton,{size:"small",onClick:()=>openEdit(row)},{default:()=>"编辑"}),
        h(NButton,{size:"small",onClick:()=>toggleCreator(row)},{default:()=>row.status==='archived'?"恢复显示":"归档"}),
        h(NButton, { size: "small", onClick: () => openBind(row) }, { default: () => "绑定平台账号" }),
        h(
          NButton,
          { size: "small", quaternary: true, onClick: () => showAccounts(row.creator_id) },
          { default: () => "账号/订阅" },
        ),
      ]);
    },
  },
];

const detailTab = ref<string | null>(null);
const detailCreatorId = ref<string | null>(null);
const accounts = ref<AccountDto[]>([]);
const subs = ref<SubscriptionDto[]>([]);

async function showAccounts(creatorId: string) {
  detailCreatorId.value = creatorId;
  detailTab.value = "accounts";
  try {
    const result = (await api.accountList(creatorId)).items;
    if(detailCreatorId.value!==creatorId)return;
    accounts.value = result;
    await loadSubs();
  } catch (e) {
    message.error(String(e));
  }
}

async function loadSubs() {
  const id=detailCreatorId.value;
  try {
    const pages=await Promise.all(accounts.value.map(a=>api.subscriptionList(a.account_id)));
    if(id===detailCreatorId.value)subs.value=pages.flatMap(p=>p.items);
  } catch (e) {
    message.error(String(e));
  }
}

async function toggleSubtitles(s:SubscriptionDto){
  try{await api.subscriptionUpdate(s.subscription_id,{capture_subtitles:!s.capture_subtitles});await loadSubs();}
  catch{message.error("字幕设置未能更新，请重试。");}
}
async function toggleChat(s:SubscriptionDto){
  try{await api.subscriptionUpdate(s.subscription_id,{capture_chat:!s.capture_chat});await loadSubs();}
  catch{message.error('弹幕采样设置未能更新，请重试。');}
}

onMounted(refresh);
</script>

<template>
  <n-card title="创作者">
    <template #header-extra>
      <n-button size="small" type="primary" @click="showCreate = true">新建创作者</n-button>
    </template>
    <n-data-table
      :columns="creatorColumns"
      :data="creators"
      :loading="loading"
      :bordered="false"
      size="small"
    />

    <n-modal :show="editCreator!==null" preset="card" title="编辑创作者" style="width:min(520px,94vw)" @update:show="v=>{if(!v)editCreator=null}">
      <n-form><n-form-item label="显示名"><n-input v-model:value="editName"/></n-form-item><n-form-item label="别名（逗号分隔）"><n-input v-model:value="editAliases"/></n-form-item><n-form-item label="标签（逗号分隔）"><n-input v-model:value="editTags"/></n-form-item><n-form-item label="备注"><n-input v-model:value="editNotes" type="textarea"/></n-form-item></n-form>
      <p>归档创作者后仍保留档案与订阅；需要停止采集时请暂停订阅。</p>
      <template #footer><n-button type="primary" @click="saveCreator">保存</n-button></template>
    </n-modal>
    <n-modal v-model:show="showCreate" preset="card" title="新建创作者" style="width: 480px">
      <n-form>
        <n-form-item label="展示名称">
          <n-input v-model:value="newDisplayName" placeholder="如：示例UP主" />
        </n-form-item>
        <n-form-item label="备注">
          <n-input v-model:value="newNotes" type="textarea" />
        </n-form-item>
      </n-form>
      <template #footer>
        <n-button type="primary" @click="create">创建</n-button>
      </template>
    </n-modal>

    <n-modal
      :show="bindCreator !== null"
      preset="card"
      title="绑定平台账号"
      style="width: 480px"
      @update:show="(v: boolean) => { if (!v) bindCreator = null }"
    >
      <n-form>
        <n-form-item label="平台">
          <n-select v-model:value="bindNamespace" :options="namespaces" />
        </n-form-item>
        <n-form-item label="平台账号 ID 或主页链接">
          <n-input v-model:value="bindKey" :disabled="binding" placeholder="Bilibili mid / 主页，或抖音用户主页 / 分享短链" />
        </n-form-item>
        <n-form-item label="访问凭证"><n-select v-model:value="bindCredential" clearable :options="bindCredentials" placeholder="访客访问" :disabled="binding"/></n-form-item>
      </n-form>
      <template #footer>
        <n-button type="primary" :loading="binding" @click="bind">解析并绑定</n-button>
      </template>
    </n-modal>

    <n-modal
      :show="subAccount !== null"
      preset="card"
      :title="editingSub?'编辑订阅':'创建订阅'"
      style="width: 480px"
      @update:show="(v: boolean) => { if (!v) subAccount = null }"
    >
      <n-form>
        <p v-if="editingSub">最近覆盖记录：{{JSON.stringify(editingSub.coverage??[])}}。来源不可见部分不计为完整历史。</p>
        <n-form-item label="范围">
          <n-select
            v-model:value="subRangeKind"
            :options="[
              { label: '仅今后新增', value: 'from_now' },
              { label: '近期历史（有界）', value: 'recent_days' },
              {label:'全部可取得历史（分批回填）',value:'all'},
              {label:'指定起止时间',value:'date_range'},
            ]"
          />
        </n-form-item>
        <n-form-item v-if="subRangeKind === 'recent_days'" label="回看天数">
          <n-input-number v-model:value="subDays" :min="1" :max="365" />
        </n-form-item>
        <n-form-item v-if="subRangeKind==='date_range'" label="起点（含） / 终点（含），本地时间"><div class="range-fields"><input v-model="subStart" type="datetime-local" aria-label="起点"><input v-model="subEnd" type="datetime-local" aria-label="终点"></div></n-form-item>
        <n-form-item v-if="!editingSub || editingSub.content_type!=='live'" label="普通内容检查间隔（秒）">
          <n-input-number v-model:value="subIntervalSeconds" :min="5" :max="86400" />
        </n-form-item>
        <n-form-item v-if="subTypes.includes('live')" label="直播检查间隔（秒）"><n-input-number v-model:value="subLiveIntervalSeconds" :min="5" :max="86400"/></n-form-item>
        <n-form-item label="内容类型"><n-select :disabled="!!editingSub" v-model:value="subTypes" multiple :options="contentTypes"/></n-form-item>
        <n-form-item label="访问凭证"><n-select v-model:value="subCredential" clearable placeholder="访客访问" :options="subCredentials"/></n-form-item>
        <n-form-item label="最低视频高度（像素，可不指定）"><n-input-number v-model:value="subHeight" :min="1" :max="16384"/></n-form-item>
        <n-form-item label="最低帧率（可不指定）"><n-input-number v-model:value="subFps" :min="1" :max="1000"/></n-form-item>
        <label class="setting"><input v-model="subPreferFps" type="checkbox">优先帧率</label>
        <label class="setting"><input v-model="subStrict" type="checkbox">只接受达标版本</label>
        <p>{{subStrict?'实际规格不足或未知时不收录该媒体，保留未收录原因和直播缺口。':'先保存当前可取得版本，记录期望与实际规格，后续补全质量。'}}</p>
        <label v-if="subTypes.includes('live')"><input v-model="subChat" type="checkbox">同时保存可取得的直播弹幕历史采样（约每 10 秒，覆盖可能不完整）</label>
        <label v-if="subTypes.includes('live')"><input v-model="subSubtitles" type="checkbox">保存 HLS 来源提供的外部字幕（若无该轨道则如实记录）</label>
      </n-form>
      <template #footer>
        <n-button type="primary" :loading="savingSub" @click="subscribe">保存订阅</n-button>
      </template>
    </n-modal>

    <n-modal
      :show="detailTab !== null"
      preset="card"
      title="平台账号与订阅"
      style="width:min(1000px,96vw)"
      @update:show="(v: boolean) => (detailTab = v ? detailTab : null)"
    >
      <n-tabs :value="detailTab ?? 'accounts'" @update:value="(k: string) => (detailTab = k)">
        <n-tab-pane name="accounts" tab="平台账号">
          <n-data-table
            :columns="[
              { title: '平台', key: 'namespace', width: 120, render: (r: AccountDto) => platformOf(r.namespace) },
              { title: '账号', key: 'native_account_key' },
              {
                title: '操作', key: 'a', width: 140,
                render: (r: AccountDto) => h(NButton, { size: 'small', onClick: () => openSubscribe(r) }, { default: () => '创建订阅' }),
              },
            ]"
            :data="accounts"
            size="small"
          />
          <n-divider />
          <AccountHistory v-for="account in accounts" :key="account.account_id" :account="account" @updated="detailCreatorId && showAccounts(detailCreatorId)"/>
          <div v-if="subs.length">
            <n-tag v-for="s in subs" :key="s.subscription_id" size="small" class="sub-tag">
              {{ s.subscription_id }}
            </n-tag>
          </div>
        </n-tab-pane>
        <n-tab-pane name="subs" tab="订阅">
          <n-button size="small" @click="loadSubs()">
            刷新全部账号订阅
          </n-button>
          <n-data-table
            :columns="[
              { title: '订阅', key: 'subscription_id' },
              {title:'间隔（秒）',key:'check_interval_seconds'},
              { title: '内容', key: 'content_type', width: 90 },
              { title: '状态', key: 'state', width: 110 },
              { title:'外部字幕',key:'capture_subtitles',render:(r:SubscriptionDto)=>r.content_type==='live'?h(NButton,{size:'small',onClick:()=>toggleSubtitles(r)},{default:()=>r.capture_subtitles?'关闭字幕':'开启字幕'}):'不适用' },
              { title:'弹幕采样', key:'capture_chat', render:(r:SubscriptionDto)=>r.content_type==='live'?h(NButton,{size:'small',onClick:()=>toggleChat(r)},{default:()=>r.capture_chat?'关闭采样':'开启采样'}):'不适用' },
              {
                title: '操作', key: 'a', width: 140,
                render: (r: SubscriptionDto) => h('div',{class:'row-actions'},[h(NButton,{size:'small',onClick:()=>editSubscription(r)},{default:()=> '配置 / 覆盖'}),h(NButton,{size:'small',onClick:()=>toggleSubscription(r)},{default:()=>r.state==='enabled'?'暂停':'恢复'}),h(NButton, { size: 'small', onClick: () => checkNow(r.subscription_id) }, { default: () => '立即检查' })]),
              },
            ]"
            :data="subs"
            size="small"
          />
        </n-tab-pane>
      </n-tabs>
    </n-modal>
  </n-card>
</template>

<style scoped>
.setting {display:block;margin:8px 0}.range-fields {display:flex;flex-wrap:wrap;gap:8px}
.row-actions {
  flex-wrap:wrap;
  display: flex;
  gap: 8px;
}
.sub-tag {
  margin-right: 8px;
}
</style>
