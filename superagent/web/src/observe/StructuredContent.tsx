import { useState } from 'react'
import Markdown from 'react-markdown'
import { Button } from '../components/ui/button'
import './structured-content.css'

export type StructuredBody =
  | { type: 'markdown'; data: { text: string } }
  | { type: 'options'; data: { prompt: string; options: { id: string; label: string }[] } }

export default function StructuredContent({ content, previewOptions = false }: { content: StructuredBody; previewOptions?: boolean }) {
  const markdown = content.type === 'options' ? content.data.prompt : content.data.text
  return (
    <div className="structured-content" data-testid="structured-content">
      <Markdown skipHtml disallowedElements={['img']} unwrapDisallowed>{markdown}</Markdown>
      {content.type === 'options' && (
        previewOptions ? <OptionPreview options={content.data.options}/> : (
          <ol>
            {content.data.options.map(option => <li key={option.id}>{option.label}</li>)}
          </ol>
        )
      )}
    </div>
  )
}

function OptionPreview({ options }: { options: { id: string; label: string }[] }) {
  const [selectedId, setSelectedId] = useState('')
  return (
    <div className="structured-option-preview">
      <div className="structured-option-buttons" role="group" aria-label="回复选项预览">
        {options.map(option => (
          <Button key={option.id} type="button" size="sm" variant="outline"
            aria-pressed={selectedId === option.id} onClick={() => setSelectedId(option.id)}>
            {option.label}
          </Button>
        ))}
      </div>
      <output>
        {selectedId ? <>选择结果预览 <code>{JSON.stringify({ optionId: selectedId })}</code></> : '选择一个选项查看结果预览'}
      </output>
    </div>
  )
}
