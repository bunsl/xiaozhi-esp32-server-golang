<template>
  <div class="meetings-page">
    <section class="meeting-hero">
      <div>
        <p class="eyebrow">MEETING INTELLIGENCE</p>
        <h2>会议纪要</h2>
        <p>集中查看实时转写、参会者和 AI 生成的会议结论</p>
      </div>
      <el-button :icon="Refresh" :loading="loading" round @click="() => loadMeetings()">刷新</el-button>
    </section>

    <div class="stat-grid">
      <div class="stat-card">
        <span>会议总数</span>
        <strong>{{ total }}</strong>
        <small>当前筛选范围</small>
      </div>
      <div class="stat-card accent">
        <span>已完成</span>
        <strong>{{ pageStats.completed }}</strong>
        <small>本页可查看纪要</small>
      </div>
      <div class="stat-card">
        <span>会议时长</span>
        <strong>{{ formatTotalDuration(pageStats.duration) }}</strong>
        <small>本页累计</small>
      </div>
      <div class="stat-card">
        <span>处理中</span>
        <strong>{{ pageStats.processing }}</strong>
        <small>{{ pageStats.processing ? '每 10 秒自动刷新' : '暂无进行中任务' }}</small>
      </div>
    </div>

    <el-card class="filter-card" shadow="never">
      <el-form :model="filters" inline @submit.prevent="applyFilters">
        <el-form-item label="搜索">
          <el-input
            v-model="filters.keyword"
            :prefix-icon="Search"
            clearable
            placeholder="会议编号、设备、纪要或转写"
            @keyup.enter="applyFilters"
          />
        </el-form-item>
        <el-form-item label="设备">
          <el-input v-model="filters.deviceId" clearable placeholder="输入设备名称" />
        </el-form-item>
        <el-form-item label="状态">
          <el-select v-model="filters.status" clearable placeholder="全部状态">
            <el-option v-for="item in statusOptions" :key="item.value" :label="item.label" :value="item.value" />
          </el-select>
        </el-form-item>
        <el-form-item label="日期">
          <el-date-picker
            v-model="filters.dateRange"
            type="daterange"
            range-separator="至"
            start-placeholder="开始日期"
            end-placeholder="结束日期"
            value-format="YYYY-MM-DD"
          />
        </el-form-item>
        <el-form-item>
          <el-button type="primary" :icon="Search" @click="applyFilters">查询</el-button>
          <el-button @click="resetFilters">重置</el-button>
        </el-form-item>
      </el-form>
    </el-card>

    <el-card class="list-card" shadow="never" v-loading="loading">
      <template #header>
        <div class="list-title">
          <div>
            <strong>全部会议</strong>
            <span>共 {{ total }} 条记录</span>
          </div>
          <el-tag v-if="pageStats.processing" type="warning" effect="plain" round>
            {{ pageStats.processing }} 条处理中
          </el-tag>
        </div>
      </template>

      <el-empty v-if="!meetings.length" description="没有找到符合条件的会议">
        <el-button v-if="hasFilters" type="primary" plain @click="resetFilters">清除筛选</el-button>
      </el-empty>
      <el-table v-else :data="meetings" row-key="id" @row-click="openMeeting">
        <el-table-column label="会议" min-width="250">
          <template #default="{ row }">
            <div class="meeting-cell">
              <span class="meeting-icon"><el-icon><Microphone /></el-icon></span>
              <div>
                <strong>{{ meetingTitle(row) }}</strong>
                <small>{{ formatDate(row.started_at) }} · {{ row.device_id }}</small>
              </div>
            </div>
          </template>
        </el-table-column>
        <el-table-column label="状态" width="116">
          <template #default="{ row }">
            <el-tag :type="statusType(row.status)" effect="light" round>
              {{ statusText(row.status) }}
            </el-tag>
          </template>
        </el-table-column>
        <el-table-column label="数据概览" min-width="180">
          <template #default="{ row }">
            <div class="metric-line">
              <span><el-icon><Clock /></el-icon>{{ formatDuration(row.duration_ms) }}</span>
              <span><el-icon><User /></el-icon>{{ row.speaker_count || 0 }} 人</span>
            </div>
          </template>
        </el-table-column>
        <el-table-column label="AI 纪要预览" min-width="320">
          <template #default="{ row }">
            <p class="summary-preview">{{ row.summary || row.transcript || processingHint(row.status) }}</p>
          </template>
        </el-table-column>
        <el-table-column width="58" align="right">
          <template #default>
            <el-icon class="row-arrow"><ArrowRight /></el-icon>
          </template>
        </el-table-column>
      </el-table>

      <el-pagination
        v-if="total > pageSize"
        v-model:current-page="page"
        :page-size="pageSize"
        :total="total"
        layout="total, prev, pager, next"
        @current-change="() => loadMeetings()"
      />
    </el-card>
  </div>
</template>

<script setup>
import { computed, onBeforeUnmount, onMounted, reactive, ref } from 'vue'
import { useRouter } from 'vue-router'
import { ArrowRight, Clock, Microphone, Refresh, Search, User } from '@element-plus/icons-vue'
import api from '../../utils/api'

const router = useRouter()
const loading = ref(false)
const meetings = ref([])
const total = ref(0)
const page = ref(1)
const pageSize = 20
let refreshTimer

const filters = reactive({
  keyword: '',
  deviceId: '',
  status: '',
  dateRange: []
})
const statusOptions = [
  { value: 'recording', label: '录音中' },
  { value: 'transcribing', label: '转写中' },
  { value: 'summarizing', label: '生成纪要' },
  { value: 'completed', label: '已完成' },
  { value: 'failed', label: '失败' }
]
const pageStats = computed(() => meetings.value.reduce((result, item) => {
  result.duration += item.duration_ms || 0
  if (item.status === 'completed') result.completed += 1
  if (['recording', 'transcribing', 'summarizing'].includes(item.status)) result.processing += 1
  return result
}, { completed: 0, processing: 0, duration: 0 }))
const hasFilters = computed(() => Boolean(
  filters.keyword || filters.deviceId || filters.status || filters.dateRange?.length
))

const loadMeetings = async ({ silent = false } = {}) => {
  if (!silent) loading.value = true
  try {
    const params = {
      page: page.value,
      page_size: pageSize,
      keyword: filters.keyword.trim() || undefined,
      device_id: filters.deviceId.trim() || undefined,
      status: filters.status || undefined
    }
    if (filters.dateRange?.length === 2) {
      params.started_after = `${filters.dateRange[0]}T00:00:00`
      params.started_before = `${filters.dateRange[1]}T23:59:59`
    }
    const { data } = await api.get('/user/meetings', { params, silentError: silent })
    meetings.value = data.data || []
    total.value = data.total || 0
  } finally {
    if (!silent) loading.value = false
  }
}
const applyFilters = () => {
  page.value = 1
  loadMeetings()
}
const resetFilters = () => {
  Object.assign(filters, { keyword: '', deviceId: '', status: '', dateRange: [] })
  applyFilters()
}
const openMeeting = row => router.push(`/user/meetings/${row.id}`)
const meetingTitle = row => row.meeting_id ? `会议 ${row.meeting_id}` : `会议记录 #${row.id}`
const formatDate = value => value ? new Date(value).toLocaleString('zh-CN', { hour12: false }) : '-'
const formatDuration = value => {
  const seconds = Math.max(0, Math.floor((value || 0) / 1000))
  return `${Math.floor(seconds / 60)}:${String(seconds % 60).padStart(2, '0')}`
}
const formatTotalDuration = value => {
  const minutes = Math.floor((value || 0) / 60000)
  return minutes < 60 ? `${minutes} 分钟` : `${Math.floor(minutes / 60)}时${minutes % 60}分`
}
const statusText = status => ({
  recording: '录音中', transcribing: '转写中', summarizing: '生成纪要', completed: '已完成', failed: '失败'
}[status] || status || '未知')
const statusType = status => status === 'completed' ? 'success' : status === 'failed' ? 'danger' : 'warning'
const processingHint = status => status === 'failed' ? '处理失败，请进入详情查看原因' : '内容正在处理中…'

onMounted(() => {
  loadMeetings()
  refreshTimer = window.setInterval(() => {
    if (pageStats.value.processing > 0) loadMeetings({ silent: true })
  }, 10000)
})
onBeforeUnmount(() => window.clearInterval(refreshTimer))
</script>

<style scoped>
.meetings-page { display: grid; gap: 16px; }
.meeting-hero {
  padding: 24px 28px; border-radius: 24px; display: flex; align-items: center; justify-content: space-between;
  background: linear-gradient(135deg, rgba(0, 122, 255, .13), rgba(90, 200, 250, .08));
  border: 1px solid rgba(0, 122, 255, .12);
}
.meeting-hero h2 { margin: 3px 0 7px; font-size: 28px; }
.meeting-hero p { margin: 0; color: var(--el-text-color-secondary); }
.eyebrow { color: var(--el-color-primary) !important; font-size: 11px; font-weight: 700; letter-spacing: .16em; }
.stat-grid { display: grid; grid-template-columns: repeat(4, minmax(0, 1fr)); gap: 12px; }
.stat-card {
  padding: 18px 20px; display: grid; gap: 5px; border-radius: 18px;
  background: var(--el-bg-color); border: 1px solid var(--el-border-color-lighter);
}
.stat-card.accent { background: linear-gradient(145deg, rgba(52, 199, 89, .12), rgba(52, 199, 89, .04)); }
.stat-card span, .stat-card small { color: var(--el-text-color-secondary); }
.stat-card span { font-size: 13px; }
.stat-card strong { font-size: 25px; line-height: 1.2; }
.stat-card small { font-size: 11px; }
.filter-card :deep(.el-card__body) { padding-bottom: 2px; }
.filter-card :deep(.el-form-item) { margin-bottom: 16px; }
.filter-card :deep(.el-input) { width: 210px; }
.filter-card :deep(.el-select) { width: 140px; }
.list-card :deep(.el-card__body) { padding-top: 0; }
.list-title, .list-title > div { display: flex; align-items: center; gap: 10px; }
.list-title { justify-content: space-between; }
.list-title span { color: var(--el-text-color-secondary); font-size: 13px; }
.el-table { cursor: pointer; }
.meeting-cell { display: flex; align-items: center; gap: 12px; }
.meeting-cell > div { min-width: 0; display: grid; gap: 5px; }
.meeting-cell small { color: var(--el-text-color-secondary); overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
.meeting-icon {
  width: 38px; height: 38px; flex: none; display: grid; place-items: center; border-radius: 12px;
  color: var(--el-color-primary); background: var(--el-color-primary-light-9);
}
.metric-line { display: flex; flex-wrap: wrap; gap: 14px; color: var(--el-text-color-secondary); font-size: 13px; }
.metric-line span { display: inline-flex; align-items: center; gap: 5px; }
.summary-preview { margin: 0; color: var(--el-text-color-regular); overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
.row-arrow { color: var(--el-text-color-placeholder); }
.el-pagination { justify-content: flex-end; margin-top: 18px; }
@media (max-width: 1100px) { .stat-grid { grid-template-columns: repeat(2, 1fr); } }
@media (max-width: 720px) {
  .meeting-hero { padding: 20px; }
  .stat-grid { grid-template-columns: 1fr 1fr; }
  .filter-card :deep(.el-form), .filter-card :deep(.el-form-item), .filter-card :deep(.el-input),
  .filter-card :deep(.el-select), .filter-card :deep(.el-date-editor) { width: 100%; }
}
</style>
