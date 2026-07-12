import { defineConfig } from 'astro/config'
import starlight from '@astrojs/starlight'

export default defineConfig({
  site: 'https://sky22333.github.io',
  base: '/hubproxy/',
  integrations: [
    starlight({
      title: 'HubProxy',
      description: 'Docker 与 GitHub 加速代理服务文档',
      defaultLocale: 'root',
      locales: {
        root: {
          label: '简体中文',
          lang: 'zh-CN',
        },
        en: {
          label: 'English',
          lang: 'en',
        },
      },
      logo: {
        alt: 'HubProxy',
        src: './src/assets/logo.svg',
      },
      social: [
        {
          icon: 'github',
          label: 'GitHub',
          href: 'https://github.com/sky22333/hubproxy',
        },
      ],
      editLink: {
        baseUrl: 'https://github.com/sky22333/hubproxy/edit/main/docs/',
      },
      sidebar: [
        {
          label: '简介',
          translations: { en: 'Introduction' },
          link: '/',
        },
        {
          label: '快速开始',
          translations: { en: 'Getting Started' },
          collapsed: true,
          items: [{ autogenerate: { directory: 'getting-started' } }],
        },
        {
          label: '部署',
          translations: { en: 'Deployment' },
          collapsed: true,
          items: [{ autogenerate: { directory: 'deployment' } }],
        },
        {
          label: '使用指南',
          translations: { en: 'Guides' },
          collapsed: true,
          items: [{ autogenerate: { directory: 'guides' } }],
        },
        {
          label: '配置',
          translations: { en: 'Configuration' },
          collapsed: true,
          items: [{ autogenerate: { directory: 'configuration' } }],
        },
        {
          label: '安全',
          translations: { en: 'Security' },
          collapsed: true,
          items: [{ autogenerate: { directory: 'security' } }],
        },
        {
          label: '常见问题',
          translations: { en: 'FAQ' },
          link: '/faq/',
        },
      ],
      customCss: ['./src/styles/custom.css'],
    }),
  ],
})
