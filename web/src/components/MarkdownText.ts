// SPDX-License-Identifier: AGPL-3.0-only
//
// Renders the limited Markdown of official messages and release notes. The
// parsed tree is turned into VNodes, so every piece of text is escaped by Vue
// and only p, br, strong, em, code, ul, li and a elements can be produced.
import { computed, defineComponent, h, type VNodeChild } from 'vue'
import { parseMarkdown, type Inline } from '@/markdown/parse'
import { useExternalLink } from '@/composables/useExternalLink'

export default defineComponent({
  name: 'MarkdownText',
  props: {
    source: { type: String, required: true },
  },
  setup(props) {
    const openLink = useExternalLink()
    const blocks = computed(() => parseMarkdown(props.source))

    function onLinkClick(event: MouseEvent, href: string) {
      // Every way of following the link goes through the confirmation.
      event.preventDefault()
      void openLink(href)
    }

    function inline(nodes: Inline[]): VNodeChild[] {
      return nodes.map((node) => {
        switch (node.type) {
          case 'text':
            return node.text
          case 'br':
            return h('br')
          case 'code':
            return h('code', node.text)
          case 'strong':
            return h('strong', inline(node.children))
          case 'em':
            return h('em', inline(node.children))
          case 'link':
            return h(
              'a',
              {
                href: node.href,
                target: '_blank',
                rel: 'noopener noreferrer',
                onClick: (event: MouseEvent) => onLinkClick(event, node.href),
                // Middle click would open a tab without asking.
                onAuxclick: (event: MouseEvent) => {
                  if (event.button === 1) onLinkClick(event, node.href)
                },
              },
              inline(node.children),
            )
        }
      })
    }

    return () =>
      h(
        'div',
        { class: 'markdown' },
        blocks.value.map((block) =>
          block.type === 'paragraph'
            ? h('p', inline(block.children))
            : h(
                'ul',
                block.items.map((item) => h('li', inline(item))),
              ),
        ),
      )
  },
})
