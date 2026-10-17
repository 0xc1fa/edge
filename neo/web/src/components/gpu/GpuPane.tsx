import AdminConsole from './AdminConsole';
import MemberLog from './MemberLog';

interface Props {
  /** 当前登录者是不是管理员 */
  canManage: boolean;
  onChanged?: () => void;
}

/**
 * GPU 调度的入口：按角色选一边的视图。
 *
 * 为什么是两个组件而不是一个组件里的两个分支：两边的读者要回答的
 * 根本不是同一个问题 ——
 *
 *   租户   「那张卡现在有没有被挂走、什么时候能轮到我」；
 *   管理员 「有几条待批准、该给谁挂卡、谁该摘了」。
 *
 * 塞进一个组件的话，每加一样东西就要多一层 if (canManage)，
 * 而那种分支长到最后没有一个人能说清「成员视角到底长什么样」。
 * 现在成员那一份可以单独读完，管理员那一份也是。
 *
 * 但**数据与口径仍然只有一份**：两边打同一个接口、用同一套状态字典
 * （gpu/Shared.tsx），服务端按角色裁剪。视图分了，业务没分。
 */
export default function GpuPane({ canManage, onChanged }: Props) {
  return canManage ? <AdminConsole onChanged={onChanged} /> : <MemberLog onChanged={onChanged} />;
}
