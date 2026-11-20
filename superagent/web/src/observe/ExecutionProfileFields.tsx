import type { ExecutionProfile } from '../workbench'

export default function ExecutionProfileFields({ profile }: { profile: ExecutionProfile }) {
  return <>
    <dt>运行时</dt><dd>{profile.runtimeRef}</dd>
    <dt>模型</dt><dd>{profile.model}</dd>
    {profile.effort && <><dt>推理强度</dt><dd>{profile.effort}</dd></>}
  </>
}
