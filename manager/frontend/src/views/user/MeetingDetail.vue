<template>
  <div class="meeting-detail" v-loading="loading">
    <div class="page-heading">
      <el-button :icon="ArrowLeft" circle @click="$router.back()" />
      <div v-if="meeting">
        <p class="eyebrow">MEETING {{ meeting.meeting_id }}</p>
        <h2>{{ formatDate(meeting.started_at) }}</h2>
        <p>{{ meeting.device_id }} · {{ formatDuration(meeting.duration_ms) }}</p>
      </div>
    </div>

    <template v-if="meeting">
      <el-alert v-if="meeting.error" :title="meeting.error" type="warning" :closable="false" />
      <div class="content-grid">
        <el-card shadow="never">
          <template #header><strong>AI 纪要</strong></template>
          <div class="summary">{{ meeting.summary || '纪要正在生成，请稍后刷新。' }}</div>
        </el-card>
        <el-card shadow="never">
          <template #header>
            <div class="card-title">
              <strong>逐字转写</strong>
              <el-tag>{{ segments.length }} 段</el-tag>
            </div>
          </template>
          <el-empty v-if="!segments.length" description="暂无转写内容" />
          <div v-else class="segments">
            <div v-for="segment in segments" :key="segment.id" class="segment">
              <div class="segment-meta">
                <el-tag effect="plain">{{ segment.speaker_name || segment.speaker_id || '发言人' }}</el-tag>
                <span>{{ formatOffset(segment.start_ms) }}</span>
              </div>
              <p>{{ segment.text }}</p>
            </div>
          </div>
        </el-card>
      </div>
    </template>
  </div>
</template>

<script setup>
import { onMounted, ref } from 'vue'
import { useRoute } from 'vue-router'
import { ArrowLeft } from '@element-plus/icons-vue'
import api from '../../utils/api'

const route = useRoute()
const loading = ref(false)
const meeting = ref(null)
const segments = ref([])

const loadMeeting = async () => {
  loading.value = true
  try {
    const { data } = await api.get(`/user/meetings/${route.params.id}`)
    meeting.value = data.meeting
    segments.value = data.segments || []
  } finally {
    loading.value = false
  }
}
const formatDate = value => value ? new Date(value).toLocaleString() : '-'
const formatDuration = value => {
  const seconds = Math.max(0, Math.floor((value || 0) / 1000))
  return `${Math.floor(seconds / 60)} 分 ${seconds % 60} 秒`
}
const formatOffset = value => {
  const seconds = Math.max(0, Math.floor((value || 0) / 1000))
  return `${String(Math.floor(seconds / 60)).padStart(2, '0')}:${String(seconds % 60).padStart(2, '0')}`
}

onMounted(loadMeeting)
</script>

<style scoped>
.meeting-detail { display: grid; gap: 16px; }
.page-heading { display: flex; align-items: center; gap: 14px; }
.page-heading h2 { margin: 2px 0 5px; }
.page-heading p { margin: 0; color: var(--el-text-color-secondary); }
.eyebrow { color: var(--el-color-primary) !important; font-size: 12px; letter-spacing: .1em; }
.content-grid { display: grid; grid-template-columns: minmax(280px, .8fr) minmax(400px, 1.2fr); gap: 16px; }
.card-title { display: flex; justify-content: space-between; align-items: center; }
.summary { white-space: pre-wrap; line-height: 1.8; }
.segments { display: grid; gap: 14px; max-height: calc(100dvh - 250px); overflow: auto; }
.segment { padding: 12px 14px; border-radius: 12px; background: var(--el-fill-color-light); }
.segment-meta { display: flex; align-items: center; gap: 10px; color: var(--el-text-color-secondary); font-size: 12px; }
.segment p { margin: 9px 0 0; line-height: 1.7; }
@media (max-width: 900px) { .content-grid { grid-template-columns: 1fr; } }
</style>
