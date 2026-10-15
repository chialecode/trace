import { h } from "vue";
import { createRouter, createWebHashHistory, type RouteRecordRaw } from "vue-router";
import { NIcon, type MenuOption } from "naive-ui";

const Home = () => import("./../views/HomeView.vue");
const Creators = () => import("./../views/CreatorsView.vue");
const Timeline = () => import("./../views/TimelineView.vue");
const Tasks = () => import("./../views/TasksView.vue");
const Accounts = () => import("./../views/AccountsView.vue");
const Modules = () => import("./../views/ModulesView.vue");
const Storage = () => import("./../views/StorageView.vue");

export const routes: RouteRecordRaw[] = [
  {path:"/acquire",name:"acquire",component:()=>import("../views/AcquireView.vue"),meta:{title:"链接归档"}},
  {path:"/upgrades",name:"upgrades",component:()=>import("../views/UpgradesView.vue"),meta:{title:"资源升级"}},
  {path:"/resources",name:"resources",component:()=>import("../views/ResourcesView.vue"),meta:{title:"资源库"}},
  { path: "/", name: "home", component: Home, meta: { title: "概览" } },
  { path: "/status", name: "status", component: () => import('../views/StatusView.vue'), meta: { title: "后台状态" } },
  { path: "/creators", name: "creators", component: Creators, meta: { title: "创作者" } },
  { path: "/timeline", name: "timeline", component: Timeline, meta: { title: "时间线" } },
  { path: "/tasks", name: "tasks", component: Tasks, meta: { title: "任务" } },
  { path: "/accounts", name: "accounts", component: Accounts, meta: { title: "账号与登录" } },
  { path: "/modules", name: "modules", component: Modules, meta: { title: "模块" } },
  { path: "/storage", name: "storage", component: Storage, meta: { title: "存储与备份" } },
];

export const navigationItems: MenuOption[] = routes.map((r) => ({
  label: String(r.meta?.title ?? String(r.name)),
  key: String(r.name),
  icon: () => h(NIcon, null, { default: () => "·" }),
}));

export const router = createRouter({
  history: createWebHashHistory(),
  routes,
});
