<template>
  <div class="meeting-detail" v-loading="loading">
    <section class="detail-hero">
      <el-button :icon="ArrowLeft" circle @click="$router.push('/user/meetings')" />
      <div class="hero-copy" v-if="meeting">
        <p class="eyebrow">MEETING {{ meeting.meeting_id }}</p>
        <h2>{{ formatDate(meeting.started_at) }}</h2>
        <p>{{ meeting.device_id }} · {{ formatDuration(meeting.duration_ms) }}</p>
      </div>
      <div class="hero-actions" v-if="meeting">
        <el-tag :type="statusType(meeting.status)" effect="light" round>
          {{ statusText(meeting.status) }}
        </el-tag>
        <el-button :icon="Refresh" circle :loading="loading" @click="loadMeeting" />
        <el-dropdown trigger="click" @command="handleExport">
          <el-button type="primary" round :icon="Download">导出</el-button>
          <template #dropdown>
            <el-dropdown-menu>
              <el-dropdown-item command="copy-summary">复制 AI 纪要</el-dropdown-item>
              <el-dropdown-item command="copy-transcript">复制逐字转写</el-dropdown-item>
              <el-dropdown-item command="copy-all" divided>复制全文</el-dropdown-item>
              <el-dropdown-item command="markdown">下载 Markdown</el-dropdown-item>
              <el-dropdown-item command="print">打印 / 另存 PDF</el-dropdown-item>
            </el-dropdown-menu>
          </template>
        </el-dropdown>
      </div>
    </section>

    <template v-if="meeting">
      <el-alert
        v-if="meeting.error"
        :title="meeting.error"
        type="warning"
        show-icon
        :closable="false"
      />

      <div class="stat-grid">
        <div class="stat-card">
          <span>会议时长</span>
          <strong>{{ formatDuration(meeting.duration_ms) }}</strong>
        </div>
        <div class="stat-card">
          <span>发言人</span>
          <strong>{{ speakerStats.length || meeting.speaker_count || 0 }}</strong>
        </div>
        <div class="stat-card">
          <span>转写段落</span>
          <strong>{{ segments.length }}</strong>
        </div>
        <div class="stat-card" :class="{ warn: meeting.missing_frames > 0 }">
          <span>丢帧</span>
          <strong>{{ meeting.missing_frames || 0 }}</strong>
        </div>
      </div>

      <el-card v-if="speakerStats.length" class="speaker-card" shadow="never">
        <template #header>
          <div class="card-title">
            <strong>发言人分析</strong>
            <el-button v-if="activeSpeaker" link type="primary" @click="activeSpeaker = ''">
              查看全部
            </el-button>
          </div>
        </template>
        <div class="speaker-chips">
          <button
            v-for="speaker in speakerStats"
            :key="speaker.name"
            class="speaker-chip"
            :class="{ active: activeSpeaker === speaker.name }"
            type="button"
            @click="toggleSpeaker(speaker.name)"
          >
            <span class="dot" :style="{ background: speaker.color }" />
            <span class="name">{{ speaker.name }}</span>
            <small>{{ speaker.count }} 段 · {{ speakerShare(speaker.count) }}</small>
          </button>
        </div>
      </el-card>

      <div class="content-grid">
        <el-card class="summary-card" shadow="never">
          <template #header>
            <div class="card-title">
              <strong>AI 纪要</strong>
              <el-button
                text
                type="primary"
                :icon="DocumentCopy"
                :disabled="!meeting.summary"
                @click="copyText(meeting.summary, '纪要已复制')"
              >
                复制
              </el-button>
            </div>
          </template>
          <div v-if="meeting.summary" class="summary">{{ meeting.summary }}</div>
          <el-empty v-else :description="emptySummaryHint" :image-size="72" />
        </el-card>

        <el-card class="transcript-card" shadow="never">
          <template #header>
            <div class="card-title">
              <strong>逐字转写</strong>
              <el-tag round>{{ filteredSegments.length }} / {{ segments.length }} 段</el-tag>
            </div>
          </template>
          <el-input
            v-model="keyword"
            :prefix-icon="Search"
            clearable
            placeholder="搜索转写内容或发言人"
          />
          <el-empty v-if="!filteredSegments.length" :description="emptyTranscriptHint" :image-size="72" />
          <div v-else class="segments">
            <article
              v-for="segment in filteredSegments"
              :key="segment.id"
              class="segment"
              :style="{ '--speaker-color': speakerColor(segment) }"
            >
              <div class="segment-meta">
                <span class="speaker-name">{{ speakerLabel(segment) }}</span>
                <span>{{ formatOffset(segment.start_ms) }}</span>
              </div>
              <p v-html="highlight(segment.text)" />
            </article>
          </div>
        </el-card>
      </div>
    </template>
  </div>
</template>

<script setup>
import { computed, onBeforeUnmount, onMounted, ref } from 'vue'
import { useRoute } from 'vue-router'
import { ElMessage } from 'element-plus'
import {
  ArrowLeft,
  DocumentCopy,
  Download,
  Refresh,
  Search
} from '@element-plus/icons-vue'
import api from '../../utils/api'

const SPEAKER_COLORS = ['#007aff', '#34c759', '#af52de', '#ff9500', '#ff2d55', '#5ac8fa', '#5856d6']

const route = useRoute()
const loading = ref(false)
const meeting = ref(null)
const segments = ref([])
const keyword = ref('')
const activeSpeaker = ref('')
let refreshTimer

const speakerLabel = segment => segment.speaker_name || segment.speaker_id || '未知发言人'
const speakerColor = segment => {
  const name = speakerLabel(segment)
  let hash = 0
  for (let i = 0; i < name.length; i += 1) hash = (hash * 31 + name.charCodeAt(i)) >>> 0
  return SPEAKER_COLORS[hash % SPEAKER_COLORS.length]
}

const speakerStats = computed(() => {
  const map = new Map()
  segments.value.forEach(segment => {
    const name = speakerLabel(segment)
    const current = map.get(name) || { name, count: 0, color: speakerColor(segment) }
    current.count += 1
    map.set(name, current)
  })
  return [...map.values()].sort((a, b) => b.count - a.count)
})

const filteredSegments = computed(() => {
  const query = keyword.value.trim().toLowerCase()
  return segments.value.filter(segment => {
    const name = speakerLabel(segment)
    if (activeSpeaker.value && name !== activeSpeaker.value) return false
    if (!query) return true
    return name.toLowerCase().includes(query) || (segment.text || '').toLowerCase().includes(query)
  })
})

const emptySummaryHint = computed(() => {
  if (!meeting.value) return '暂无纪要'
  if (meeting.value.status === 'failed') return '纪要生成失败，请查看上方错误信息'
  if (['recording', 'transcribing', 'summarizing'].includes(meeting.value.status)) {
    return '纪要正在生成，页面会自动刷新'
  }
  return '这次会议还没有生成纪要'
})

const emptyTranscriptHint = computed(() => {
  if (keyword.value || activeSpeaker.value) return '没有匹配的转写内容'
  if (['recording', 'transcribing'].includes(meeting.value?.status)) return '转写进行中，稍后自动更新'
  return '暂无转写内容'
})

const speakerShare = count => {
  const total = segments.value.length || 1
  return `${Math.round((count / total) * 100)}%`
}

const loadMeeting = async ({ silent = false } = {}) => {
  if (!silent) loading.value = true
  try {
    const { data } = await api.get(`/user/meetings/${route.params.id}`, { silentError: silent })
    meeting.value = data.meeting
    segments.value = data.segments || []
  } finally {
    if (!silent) loading.value = false
  }
}

const toggleSpeaker = name => {
  activeSpeaker.value = activeSpeaker.value === name ? '' : name
}

const formatDate = value => value ? new Date(value).toLocaleString('zh-CN', { hour12: false }) : '-'
const formatDuration = value => {
  const seconds = Math.max(0, Math.floor((value || 0) / 1000))
  const hours = Math.floor(seconds / 3600)
  const minutes = Math.floor((seconds % 3600) / 60)
  if (hours) return `${hours} 小时 ${minutes} 分`
  return `${minutes} 分 ${seconds % 60} 秒`
}
const formatOffset = value => {
  const seconds = Math.max(0, Math.floor((value || 0) / 1000))
  return `${String(Math.floor(seconds / 60)).padStart(2, '0')}:${String(seconds % 60).padStart(2, '0')}`
}
const statusText = status => ({
  recording: '录音中', transcribing: '转写中', summarizing: '生成纪要', completed: '已完成', failed: '失败'
}[status] || status || '未知')
const statusType = status => status === 'completed' ? 'success' : status === 'failed' ? 'danger' : 'warning'

const escapeHtml = value => String(value || '')
  .replace(/&/g, '&amp;')
  .replace(/</g, '&lt;')
  .replace(/>/g, '&gt;')
const highlight = text => {
  const safe = escapeHtml(text)
  const query = keyword.value.trim()
  if (!query) return safe
  const pattern = query.replace(/[.*+?^${}()|[\]\\]/g, '\\$&')
  return safe.replace(new RegExp(pattern, 'gi'), match => `<mark>${match}</mark>`)
}

const transcriptText = () => segments.value
  .map(segment => `[${formatOffset(segment.start_ms)}] ${speakerLabel(segment)}：${segment.text}`)
  .join('\n')

const fullMarkdown = () => {
  const item = meeting.value || {}
  const lines = [
    `# 会议纪要 ${item.meeting_id || item.id || ''}`,
    '',
    `- 开始时间：${formatDate(item.started_at)}`,
    `- 设备：${item.device_id || '-'}`,
    `- 时长：${formatDuration(item.duration_ms)}`,
    `- 状态：${statusText(item.status)}`,
    `- 发言人：${speakerStats.value.length || item.speaker_count || 0} 人`,
    '',
    '## AI 纪要',
    '',
    item.summary || '暂无纪要',
    '',
    '## 逐字转写',
    '',
    transcriptText() || '暂无转写'
  ]
  return lines.join('\n')
}

const copyText = async (text, success = '已复制') => {
  if (!text) {
    ElMessage.warning('暂无可复制内容')
    return
  }
  try {
    await navigator.clipboard.writeText(text)
    ElMessage.success(success)
  } catch {
    ElMessage.error('复制失败，请检查浏览器权限')
  }
}

const downloadMarkdown = () => {
  const blob = new Blob([fullMarkdown()], { type: 'text/markdown;charset=utf-8' })
  const url = URL.createObjectURL(blob)
  const link = document.createElement('a')
  const stamp = meeting.value?.meeting_id || meeting.value?.id || 'meeting'
  link.href = url
  link.download = `会议纪要-${stamp}.md`
  document.body.appendChild(link)
  link.click()
  link.remove()
  URL.revokeObjectURL(url)
  ElMessage.success('已开始下载 Markdown')
}

const handleExport = command => {
  if (command === 'copy-summary') return copyText(meeting.value?.summary, '纪要已复制')
  if (command === 'copy-transcript') return copyText(transcriptText(), '转写已复制')
  if (command === 'copy-all') return copyText(fullMarkdown(), '全文已复制')
  if (command === 'markdown') return downloadMarkdown()
  window.print()
}

onMounted(() => {
  loadMeeting()
  refreshTimer = window.setInterval(() => {
    const status = meeting.value?.status
    if (['recording', 'transcribing', 'summarizing'].includes(status)) {
      loadMeeting({ silent: true })
    }
  }, 10000)
})
onBeforeUnmount(() => window.clearInterval(refreshTimer))
</script>

<style scoped>
.meeting-detail { display: grid; gap: 16px; }
.detail-hero {
  padding: 20px 24px; border-radius: 24px; display: flex; align-items: center; gap: 14px;
  background: linear-gradient(135deg, rgba(0, 122, 255, .13), rgba(90, 200, 250, .08));
  border: 1px solid rgba(0, 122, 255, .12);
}
.hero-copy { min-width: 0; flex: 1; }
.hero-copy h2 { margin: 3px 0 6px; }
.hero-copy p, .eyebrow { margin: 0; }
.hero-copy p { color: var(--el-text-color-secondary); }
.eyebrow { color: var(--el-color-primary) !important; font-size: 11px; font-weight: 700; letter-spacing: .12em; }
.hero-actions { display: flex; align-items: center; gap: 8px; flex: none; }
.stat-grid { display: grid; grid-template-columns: repeat(4, minmax(0, 1fr)); gap: 12px; }
.stat-card {
  padding: 16px 18px; display: grid; gap: 6px; border-radius: 18px;
  background: var(--el-bg-color); border: 1px solid var(--el-border-color-lighter);
}
.stat-card.warn { background: rgba(255, 149, 0, .08); }
.stat-card span { color: var(--el-text-color-secondary); font-size: 13px; }
.stat-card strong { font-size: 22px; }
.card-title { display: flex; justify-content: space-between; align-items: center; gap: 12px; }
.speaker-chips { display: flex; flex-wrap: wrap; gap: 10px; }
.speaker-chip {
  min-width: 168px; padding: 12px 14px; border-radius: 16px; text-align: left; cursor: pointer;
  border: 1px solid var(--el-border-color-lighter); background: var(--el-fill-color-blank);
  display: grid; gap: 4px;
}
.speaker-chip.active, .speaker-chip:hover { border-color: var(--el-color-primary); background: var(--el-color-primary-light-9); }
.speaker-chip .dot { width: 8px; height: 8px; border-radius: 50%; display: inline-block; margin-right: 6px; }
.speaker-chip .name { font-weight: 600; }
.speaker-chip small { color: var(--el-text-color-secondary); }
.content-grid { display: grid; grid-template-columns: minmax(280px, .85fr) minmax(420px, 1.15fr); gap: 16px; }
.summary { white-space: pre-wrap; line-height: 1.85; font-size: 15px; }
.transcript-card :deep(.el-input) { margin-bottom: 14px; }
.segments { display: grid; gap: 12px; max-height: calc(100dvh - 280px); overflow: auto; padding-right: 4px; }
.segment {
  padding: 12px 14px 12px 16px; border-radius: 14px; background: var(--el-fill-color-light);
  border-left: 3px solid var(--speaker-color);
}
.segment-meta { display: flex; align-items: center; justify-content: space-between; gap: 10px; font-size: 12px; color: var(--el-text-color-secondary); }
.speaker-name { color: var(--speaker-color); font-weight: 700; }
.segment p { margin: 8px 0 0; line-height: 1.75; }
.segment :deep(mark) { background: #ffe58f; color: inherit; padding: 0 2px; border-radius: 4px; }
@media (max-width: 980px) {
  .stat-grid, .content-grid { grid-template-columns: 1fr; }
  .detail-hero { flex-wrap: wrap; }
  .hero-actions { width: 100%; justify-content: flex-end; }
}
@media print {
  .hero-actions, .transcript-card :deep(.el-input), .speaker-card { display: none !important; }
  .content-grid, .stat-grid { grid-template-columns: 1fr; }
  .segments { max-height: none; overflow: visible; }
}
</style>
