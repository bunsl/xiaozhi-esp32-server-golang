<template>
  <div class="meetings-page">
    <div class="page-heading">
      <div>
        <p class="eyebrow">MEETING INTELLIGENCE</p>
        <h2>会议记录</h2>
        <p>查看转写、说话人和 AI 纪要</p>
      </div>
      <el-button :icon="Refresh" circle @click="loadMeetings" />
    </div>

    <el-card shadow="never" v-loading="loading">
      <el-empty v-if="!meetings.length" description="暂无会议记录" />
      <el-table v-else :data="meetings" @row-click="openMeeting">
        <el-table-column label="开始时间" min-width="170">
          <template #default="{ row }">{{ formatDate(row.started_at) }}</template>
        </el-table-column>
        <el-table-column prop="device_id" label="设备" min-width="150" />
        <el-table-column label="时长" width="100">
          <template #default="{ row }">{{ formatDuration(row.duration_ms) }}</template>
        </el-table-column>
        <el-table-column label="状态" width="120">
          <template #default="{ row }">
            <el-tag :type="statusType(row.status)">{{ statusText(row.status) }}</el-tag>
          </template>
        </el-table-column>
        <el-table-column prop="speaker_count" label="人数" width="80" />
        <el-table-column label="纪要" min-width="260" show-overflow-tooltip>
          <template #default="{ row }">{{ row.summary || row.transcript || '处理中…' }}</template>
        </el-table-column>
      </el-table>
      <el-pagination
        v-if="total > pageSize"
        v-model:current-page="page"
        :page-size="pageSize"
        :total="total"
        layout="total, prev, pager, next"
        @current-change="loadMeetings"
      />
    </el-card>
  </div>
</template>

<script setup>
import { onMounted, ref } from 'vue'
import { useRouter } from 'vue-router'
import { Refresh } from '@element-plus/icons-vue'
import api from '../../utils/api'

const router = useRouter()
const loading = ref(false)
const meetings = ref([])
const total = ref(0)
const page = ref(1)
const pageSize = 20

const loadMeetings = async () => {
  loading.value = true
  try {
    const { data } = await api.get('/user/meetings', { params: { page: page.value, page_size: pageSize } })
    meetings.value = data.data || []
    total.value = data.total || 0
  } finally {
    loading.value = false
  }
}

const openMeeting = row => router.push(`/user/meetings/${row.id}`)
const formatDate = value => value ? new Date(value).toLocaleString() : '-'
const formatDuration = value => {
  const seconds = Math.max(0, Math.floor((value || 0) / 1000))
  return `${Math.floor(seconds / 60)}:${String(seconds % 60).padStart(2, '0')}`
}
const statusText = status => ({
  recording: '录音中', transcribing: '转写中', summarizing: '生成纪要', completed: '已完成', failed: '失败'
}[status] || status)
const statusType = status => status === 'completed' ? 'success' : status === 'failed' ? 'danger' : 'warning'

onMounted(loadMeetings)
</script>

<style scoped>
.meetings-page { display: grid; gap: 16px; }
.page-heading { display: flex; align-items: center; justify-content: space-between; }
.page-heading h2 { margin: 2px 0 6px; }
.page-heading p { margin: 0; color: var(--el-text-color-secondary); }
.eyebrow { color: var(--el-color-primary) !important; font-size: 12px; letter-spacing: .14em; }
.el-table { cursor: pointer; }
.el-pagination { justify-content: flex-end; margin-top: 18px; }
</style>
