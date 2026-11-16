<!-- SPDX-License-Identifier: AGPL-3.0-only -->
<script setup lang="ts">
// Places to go: the roots, the user's favourite directories and the trash.
import { useI18n } from 'vue-i18n'
import { NButton, NIcon, NText } from 'naive-ui'
import { ArchiveOutline, CloseOutline, DesktopOutline, GlobeOutline, StarOutline, TrashOutline } from '@vicons/ionicons5'
import type { FileRef, FileRoot } from '@/api/files'
import { absolutePath, baseName, rootLabel } from '@/files/paths'

defineProps<{
  roots: FileRoot[]
  root: string
  path: string
  trash: boolean
  favorites: FileRef[]
}>()
const emit = defineEmits<{
  go: [root: string, path: string]
  openTrash: []
  removeFavorite: [favorite: FileRef]
}>()

const { t } = useI18n()

const ICONS: Record<string, typeof GlobeOutline> = { sites: GlobeOutline, backups: ArchiveOutline, system: DesktopOutline }
</script>

<template>
  <nav class="side" :aria-label="t('files.side.label')">
    <NText depth="3" tag="div" class="side__heading">{{ t('files.side.places') }}</NText>
    <button
      v-for="entry in roots"
      :key="entry.id"
      type="button"
      class="side__item"
      :class="{ 'side__item--active': !trash && entry.id === root && path === '' }"
      :disabled="!entry.available"
      :title="entry.available ? entry.path : t('files.side.unavailable')"
      @click="emit('go', entry.id, '')"
    >
      <NIcon size="16"><component :is="ICONS[entry.id] ?? GlobeOutline" /></NIcon>
      <span class="side__label">
        <span>{{ rootLabel(entry.id) }}</span>
        <span class="side__path" dir="ltr">{{ entry.path }}</span>
      </span>
    </button>
    <button
      type="button"
      class="side__item"
      :class="{ 'side__item--active': trash }"
      @click="emit('openTrash')"
    >
      <NIcon size="16"><TrashOutline /></NIcon>
      <span class="side__label">{{ t('files.trash.title', { root: rootLabel(root) }) }}</span>
    </button>

    <NText depth="3" tag="div" class="side__heading">{{ t('files.side.favorites') }}</NText>
    <NText v-if="favorites.length === 0" depth="3" tag="p" class="side__empty">{{ t('files.side.noFavorites') }}</NText>
    <div v-for="favorite in favorites" :key="`${favorite.root}:${favorite.path}`" class="side__favorite">
      <button
        type="button"
        class="side__item"
        :class="{ 'side__item--active': !trash && favorite.root === root && favorite.path === path }"
        :title="absolutePath(favorite.root, favorite.path)"
        @click="emit('go', favorite.root, favorite.path)"
      >
        <NIcon size="16"><StarOutline /></NIcon>
        <span class="side__label">
          <span>{{ favorite.path ? baseName(favorite.path) : rootLabel(favorite.root) }}</span>
          <span class="side__path" dir="ltr">{{ absolutePath(favorite.root, favorite.path) }}</span>
        </span>
      </button>
      <NButton
        size="tiny"
        quaternary
        circle
        class="side__remove"
        :aria-label="t('files.side.removeFavorite')"
        @click="emit('removeFavorite', favorite)"
      >
        <template #icon>
          <NIcon><CloseOutline /></NIcon>
        </template>
      </NButton>
    </div>
  </nav>
</template>

<style scoped>
.side {
  display: flex;
  flex-direction: column;
  gap: 2px;
}

.side__heading {
  margin-block: 12px 4px;
  font-size: 12px;
  text-transform: uppercase;
  letter-spacing: 0.04em;
}

.side__heading:first-child {
  margin-block-start: 0;
}

.side__item {
  display: flex;
  align-items: center;
  gap: 8px;
  inline-size: 100%;
  min-inline-size: 0;
  padding: 6px 8px;
  border: 0;
  border-radius: 3px;
  background: transparent;
  color: inherit;
  font: inherit;
  text-align: start;
  cursor: pointer;
}

.side__item:hover:not(:disabled),
.side__item:focus-visible {
  background: rgba(128, 128, 128, 0.12);
}

.side__item:disabled {
  cursor: not-allowed;
  opacity: 0.5;
}

.side__item--active {
  background: rgba(24, 160, 88, 0.12);
}

.side__label {
  display: flex;
  flex-direction: column;
  min-inline-size: 0;
}

.side__label > span {
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}

.side__path {
  font-family: ui-monospace, SFMono-Regular, Menlo, Consolas, monospace;
  font-size: 11px;
  opacity: 0.65;
}

.side__empty {
  margin: 0;
  padding-inline: 8px;
  font-size: 12px;
}

.side__favorite {
  position: relative;
  display: flex;
  align-items: center;
}

.side__remove {
  flex: none;
}
</style>
