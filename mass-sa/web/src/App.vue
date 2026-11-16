<script setup lang="ts">
import { computed, ref } from 'vue'
import { useQuery } from '@tanstack/vue-query'
import { RouterLink, RouterView, useRoute, useRouter } from 'vue-router'
import Button from 'primevue/button'
import Select from 'primevue/select'
import Popover from 'primevue/popover'
import { readProjects } from './api'
import { queryText, usePolling } from './polling'

const route = useRoute()
const router = useRouter()
const interval = usePolling()
const help = ref<InstanceType<typeof Popover>>()
const projects = useQuery({ queryKey: ['projects'], queryFn: ({ signal }) => readProjects(signal), refetchOnWindowFocus: computed(() => interval.value !== false), refetchOnReconnect: computed(() => interval.value !== false), refetchInterval: interval })
const project = computed(() => queryText(route.query.project))
const projectOptions = computed(() => [{ id: '', name: '全部项目' }, ...(project.value && !projects.data.value?.some(p => p.id === project.value) ? [{ id: project.value, name: `${project.value}（当前筛选）` }] : []), ...(projects.data.value || [])])
const paused = computed(() => route.query.refresh === 'paused')
const destinations = [ ['triggers', '事件', 'pi-inbox'], ['routers', '路由器', 'pi-sitemap'], ['tickets', '工作项', 'pi-briefcase'], ['resources', '执行会话', 'pi-desktop'] ]
function selectProject(value: string) {
  router.push({ query: { ...route.query, project: value || undefined, cursor: undefined, selected: undefined, selectedProject: undefined, replyCursor: undefined, factCursor: undefined, ticketOutputCursor: undefined } })
}
function toggleRefresh() { router.replace({ query: { ...route.query, refresh: paused.value ? undefined : 'paused' } }) }
</script>

<template>
  <a class="skip" href="#main">跳到主要内容</a>
  <div class="app-shell">
    <aside class="sidebar">
      <RouterLink class="brand" :to="{ name: 'triggers', query: { project: project || undefined, refresh: route.query.refresh } }"><span class="brand-mark">M</span><span>MASS<span class="brand-sub">Observer</span></span></RouterLink>
      <nav class="side-nav topnav" aria-label="主导航">
        <RouterLink v-for="[name, label, icon] in destinations" :key="name" class="nav-item" :class="{ active: route.name === name }" :aria-label="label" :to="{ name, query: { project: project || undefined, refresh: route.query.refresh } }" :aria-current="route.name === name ? 'page' : undefined"><i :class="['pi', icon]" aria-hidden="true"></i><span>{{ label }}</span></RouterLink>
      </nav>
      <div class="sidebar-bottom"><div class="connection"><i class="pi pi-server" aria-hidden="true"></i><div><strong>本地只读观测</strong><span>业务状态与渠道结果分别展示</span></div></div></div>
    </aside>
    <div class="main-shell">
      <header class="topbar">
        <div class="project-switch"><i class="pi pi-folder" aria-hidden="true"></i><Select input-id="project" aria-label="项目" class="project-select" :model-value="project" :options="projectOptions" option-label="name" option-value="id" @update:model-value="selectProject" /></div>
        <div class="topbar-actions"><span class="refresh-status muted">{{ paused ? '自动刷新已暂停' : route.query.cursor ? '翻页期间暂停自动刷新' : '每 5 秒刷新 · 后台暂停' }}</span><Button :icon="paused ? 'pi pi-play' : 'pi pi-pause'" :aria-label="paused ? '继续自动刷新' : '暂停自动刷新'" :aria-pressed="paused" severity="secondary" text @click="toggleRefresh" /><Button icon="pi pi-question-circle" label="使用说明" severity="secondary" text @click="help?.toggle($event)" /></div>
      </header>
      <Popover ref="help"><div class="help-content"><h3>使用说明</h3><dl class="facts"><dt>事件</dt><dd>查看收到的消息、路由依据和固定投递目标。</dd><dt>工作项</dt><dd>查看业务目标、当前负责会话和派工历史。</dd><dt>回复</dt><dd>显式回复、业务事实和各消费者渠道确认分别呈现。</dd><dt>刷新</dt><dd>每 5 秒刷新，页面后台暂停；可手动暂停。翻页保持快照，刷新回到第一页。</dd></dl><p class="detail-note">页面只读。持久引用、请求配置与 massd 实际观察分别展示；时间使用本地时区，悬停可查看 ISO 原值。</p></div></Popover>
      <main id="main" tabindex="-1"><p v-if="projects.isError.value" class="error-message" role="status">项目列表读取失败；保留上次查询结果，可继续查看或刷新页面。</p><RouterView /></main>
    </div>
  </div>
</template>
