import { useState } from 'react'
import { Check, Link2 } from 'lucide-react'
import { Button } from '../components/ui/button'

export default function CopyLink({ href }: { href: string }) {
  const [result, setResult] = useState('')
  async function copy() {
    try {
      await navigator.clipboard.writeText(new URL(href, location.origin).href)
      setResult('已复制')
    } catch {
      setResult('复制失败')
    }
  }
  return (
    <Button variant="ghost" size="sm" onClick={copy}>
      {result === '已复制' ? <Check /> : <Link2 />}
      <span aria-live="polite">{result || '复制链接'}</span>
    </Button>
  )
}
