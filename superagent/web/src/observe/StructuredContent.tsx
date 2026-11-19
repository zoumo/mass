import Markdown from 'react-markdown'
import './structured-content.css'

export type StructuredBody =
  | { type: 'markdown'; data: { text: string } }
  | { type: 'options'; data: { prompt: string; options: { id: string; label: string }[] } }

export default function StructuredContent({ content }: { content: StructuredBody }) {
  const markdown = content.type === 'options' ? content.data.prompt : content.data.text
  return (
    <div className="structured-content" data-testid="structured-content">
      <Markdown skipHtml disallowedElements={['img']} unwrapDisallowed>{markdown}</Markdown>
      {content.type === 'options' && (
        <ol>
          {content.data.options.map(option => <li key={option.id}>{option.label}</li>)}
        </ol>
      )}
    </div>
  )
}
