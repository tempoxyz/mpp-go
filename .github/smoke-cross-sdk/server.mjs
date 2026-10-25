// Minimal mppx server for the cross-SDK smoke test: the Go client pays it.
import * as http from 'node:http'

import { Mppx, tempo } from 'mppx/server'
import { createClient, defineChain, http as viemHttp } from 'viem'
import { tempoLocalnet } from 'viem/chains'

const rpcUrl = process.env.TEMPO_RPC_URL ?? 'http://localhost:8545'
const port = Number(process.env.PORT ?? 3000)

const chain = defineChain({ ...tempoLocalnet, rpcUrls: { default: { http: [rpcUrl] } } })
const client = createClient({ chain, transport: viemHttp(rpcUrl) })

const mppx = Mppx.create({
  secretKey: 'smoke-cross-sdk-secret-minimum-32-bytes',
  methods: [
    tempo.charge({
      chainId: chain.id,
      currency: '0x20c0000000000000000000000000000000000000', // pathUSD on the localnet
      getClient: () => client,
      recipient: '0x70997970c51812dc3a010c7d01b50e0d17dc79c8', // devnet.Recipient in mpp-go
    }),
  ],
})

const charge = Mppx.toNodeListener(mppx.charge({ amount: '0.50', description: 'Cross-SDK smoke' }))

http
  .createServer(async (req, res) => {
    try {
      // URL.parse returns null for a malformed request target such as `http://[invalid/`.
      const url = URL.parse(req.url ?? '/', 'http://localhost')
      if (!url) {
        res.writeHead(400).end()
        return
      }
      if (url.pathname === '/health') {
        res.writeHead(200, { 'Content-Type': 'application/json' })
        res.end(JSON.stringify({ status: 'ok' }))
        return
      }
      if (url.pathname !== '/paid') {
        res.writeHead(404).end()
        return
      }
      const result = await charge(req, res)
      if (result.status === 402) return
      res.writeHead(200, { 'Content-Type': 'application/json' })
      res.end(JSON.stringify({ data: 'paid content', payer: 'mppx' }))
    } catch (error) {
      console.error(error)
      if (!res.headersSent) res.writeHead(500)
      res.end()
    }
  })
  .listen(port, '127.0.0.1', () => console.log(`mppx smoke server listening on http://127.0.0.1:${port}`))
