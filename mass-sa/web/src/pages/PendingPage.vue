<script setup lang="ts">
import { useRoute, RouterLink } from 'vue-router'
const route = useRoute()
</script>
<template>
  <div class="page-head"><div><h1>页面不可用</h1><p>此地址不属于 Observer 的查询页面。</p></div></div>
  <section class="state-display" aria-label="页面未找到"><h2>未找到页面</h2><p>从 Triggers 重新选择事件，或使用顶部导航。</p><RouterLink class="touch-link" :to="{ name: 'triggers', query: { project: route.query.project, refresh: route.query.refresh } }">查看 Triggers</RouterLink></section>
</template>
