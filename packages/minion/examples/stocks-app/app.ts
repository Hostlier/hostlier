import * as z from 'zod/mini';
import { buildMinion, defineAction, defineOperation, defineServices } from '@fused/minion';
import { parseHistory, parseDailyQuote, pointSchema, quoteSchema } from './data.js';

const services = defineServices({ markets: { getStockData: defineOperation({
  endpoint: 'getStockData',
  input: z.object({ function: z.enum(['GLOBAL_QUOTE', 'TIME_SERIES_DAILY']), symbol: z.literal('IBM') }),
  output: z.unknown(),
}) } });
const dashboardSchema = z.object({ quote: quoteSchema, history: z.array(pointSchema), historyAvailable: z.boolean(), fetchedAt: z.string(), provider: z.literal('Alpha Vantage'), demo: z.literal(true) });
type Dashboard = z.infer<typeof dashboardSchema>;
let cached: { expiresAt: number; value: Dashboard } | undefined;

export default buildMinion({ actions: {
  getDashboard: defineAction({
    // Anonymous read-only access is deliberate for this local, fixed-symbol demo.
    access: 'public',
    input: z.object({ symbol: z.literal('IBM') }),
    output: dashboardSchema,
    /** Fetch through Engine's scoped gRPC operation; the provider credential stays in Fused. */
    async execute({ input }) {
      if (cached && cached.expiresAt > Date.now()) return cached.value;
      const daily = await services.markets.getStockData({ function: 'TIME_SERIES_DAILY', symbol: input.symbol });
      const history = parseHistory(daily);
      const quote = parseDailyQuote(daily);
      const value: Dashboard = { quote, history, historyAvailable: history.length > 0, fetchedAt: new Date().toISOString(), provider: 'Alpha Vantage', demo: true };
      cached = { expiresAt: Date.now() + 60_000, value };
      return value;
    },
  }),
} });
