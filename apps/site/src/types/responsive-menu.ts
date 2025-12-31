import { MenuItem } from 'primevue/menuitem';

export interface ResponsiveMenuProps {
  // MenuItem 格式支援
  items?: MenuItem[];
  dangerItems?: MenuItem[];

  // 自定義模板支援
  useCustomContent?: boolean; // 標記使用自定義 slot

  // 桌面版專用
  appendTo?: string | HTMLElement; // Popup 掛載位置（預設：'body'）

  // 手機版專用（Dialog props）
  header?: string;
  dialogClass?: string;

  // 通用
  disabled?: boolean;
}

export interface ResponsiveMenuEmits {
  (e: 'open'): void;
  (e: 'close'): void;
  (e: 'update:visible', value: boolean): void;
}

export interface ResponsiveMenuSlots {
  default(): any; // 觸發按鈕內容
  menu?(): any; // 自定義菜單內容
}
