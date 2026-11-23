import { type LinkProps } from '@tanstack/react-router'
import { type TranslationKey } from '@/lib/i18n/en'

type BaseNavItem = {
  title: TranslationKey
  badge?: string
  icon?: React.ElementType
}

type NavLink = BaseNavItem & {
  url: LinkProps['to'] | (string & {})
  items?: never
}

type NavCollapsible = BaseNavItem & {
  items: (BaseNavItem & { url: LinkProps['to'] | (string & {}) })[]
  url?: never
}

type NavItem = NavCollapsible | NavLink

type NavGroup = {
  title: TranslationKey
  items: NavItem[]
}

export type { NavGroup, NavItem, NavCollapsible, NavLink }
