export type ContentKind = 'video' | 'image' | 'activity' | 'live' | 'article' | 'collection';
export interface FeedEntry {
  id: string; platform: string; platformLabel: string; kind: ContentKind;
  title: string; body: string; time: string; day: string; tags: string[]; art: string;
  duration?: string; views?: string; comments?: string; likes?: string;
  saveLabel?: string; qualityLabel?: string;
  coverUrl?: string; sourceUrl?: string | null; sourceKey?: string;
  creatorName?: string | null; accountName?: string | null; precision?: string;
}
// Explicit visual-preview fixtures. Never used as daemon data or download targets.
export const previewEntries: FeedEntry[] = [
  { id:'preview-1',platform:'bilibili',platformLabel:'Bilibili',kind:'video',title:'【新番】《葬送的芙莉莲》第二季 先导PV | 2026年放送决定！',body:'在那片广阔的世界上，与各式各样的相遇与别离。\n“又要开始新的旅程了吗……”',time:'2小时前',day:'2025-09-24',tags:['动画','新番','葬送的芙莉莲'],art:'feature',duration:'01:32',views:'128.4万',comments:'4.2万',likes:'12.6万' },
  { id:'preview-2',platform:'youtube',platformLabel:'YouTube',kind:'video',title:'【4K】新海诚 经典场景合集 | 你的名字。天气之子 画面集',body:'剪辑了新海诚历年来的经典场景，配上原声BGM，希望大家喜欢！',time:'4小时前',day:'2025-09-24',tags:['影视剪辑'],art:'city',duration:'12:48',views:'86.7万',comments:'1.3万',likes:'9.4万' },
  { id:'preview-3',platform:'x',platformLabel:'X (Twitter)',kind:'activity',title:'新的周边图好好看！',body:'这次的立绘和插画都太棒了，已经预定了～',time:'1天前',day:'2025-09-23',tags:['ふりすぽ','立牌'],art:'character',views:'2.4万',comments:'368',likes:'1.5万' },
  { id:'preview-4',platform:'instagram',platformLabel:'Instagram',kind:'image',title:'今天的日常 ✨',body:'海边的风真的很舒服。',time:'1天前',day:'2025-09-23',tags:['生活'],art:'sea' },
];
export const platforms = [
  {id:'bilibili',label:'Bilibili',mark:'bili',count:12},
  {id:'youtube',label:'YouTube',mark:'▶',count:8},
  {id:'x',label:'X (Twitter)',mark:'𝕏',count:5},
  {id:'instagram',label:'Instagram',mark:'◎',count:3},
  {id:'douyin',label:'抖音',mark:'♪',count:2},
];
export function filterFeed(entries: FeedEntry[], query: string, kind: string, platform: string, oldest: boolean) {
  const keyword = query.trim().toLocaleLowerCase();
  const result = entries.filter(entry => (!kind || entry.kind === kind) && (!platform || entry.platform === platform) && (!keyword || `${entry.title} ${entry.body} ${entry.tags.join(' ')} ${entry.platformLabel}`.toLocaleLowerCase().includes(keyword)));
  return oldest ? [...result].reverse() : result;
}
