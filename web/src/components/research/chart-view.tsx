"use client";

import { Bar, BarChart, CartesianGrid, Cell, Legend, Line, LineChart, Pie, PieChart, ResponsiveContainer, Tooltip, XAxis, YAxis } from "recharts";

import type { Schemas } from "@/lib/api/client";

type Chart = Schemas["Chart"];

const COLORS = ["#2563eb", "#059669", "#d97706", "#7c3aed", "#dc2626", "#0891b2"];

/** Renders a report chart. The data always comes from a tool result (the chart's dataset), never from the model. */
export function ChartView({ chart }: { chart: Chart }) {
  const data = chart.data as Record<string, string | number>[];
  return (
    <figure className="my-4 rounded-lg border p-3">
      <figcaption className="mb-2 flex items-baseline justify-between gap-2 text-sm font-medium">
        <span>{chart.title}</span>
        <span className="text-xs font-normal text-muted-foreground">Data: {chart.dataset}</span>
      </figcaption>
      {chart.type === "table" ? <DataTable data={data} /> : (
        <div className="h-64 w-full">
          <ResponsiveContainer width="100%" height="100%">
            {chart.type === "line" ? (
              <LineChart data={data}>
                <CartesianGrid strokeDasharray="3 3" className="stroke-muted" />
                <XAxis dataKey={chart.x} fontSize={11} />
                <YAxis fontSize={11} allowDecimals={false} />
                <Tooltip />
                {chart.y.length > 1 && <Legend />}
                {chart.y.map((y, i) => <Line key={y} type="monotone" dataKey={y} stroke={COLORS[i % COLORS.length]} strokeWidth={2} dot={false} />)}
              </LineChart>
            ) : chart.type === "pie" ? (
              <PieChart>
                <Tooltip />
                <Legend />
                <Pie data={data} dataKey={chart.y[0]} nameKey={chart.x} outerRadius="80%">
                  {data.map((_, i) => <Cell key={i} fill={COLORS[i % COLORS.length]} />)}
                </Pie>
              </PieChart>
            ) : (
              <BarChart data={data}>
                <CartesianGrid strokeDasharray="3 3" className="stroke-muted" />
                <XAxis dataKey={chart.x} fontSize={11} interval={0} angle={data.length > 6 ? -30 : 0} textAnchor={data.length > 6 ? "end" : "middle"} height={data.length > 6 ? 60 : 30} />
                <YAxis fontSize={11} allowDecimals={false} />
                <Tooltip />
                {chart.y.length > 1 && <Legend />}
                {chart.y.map((y, i) => <Bar key={y} dataKey={y} fill={COLORS[i % COLORS.length]} radius={[3, 3, 0, 0]} />)}
              </BarChart>
            )}
          </ResponsiveContainer>
        </div>
      )}
    </figure>
  );
}

function DataTable({ data }: { data: Record<string, string | number>[] }) {
  const cols = data.length ? Object.keys(data[0]) : [];
  return (
    <div className="overflow-x-auto">
      <table className="w-full text-xs">
        <thead><tr className="border-b">{cols.map((c) => <th key={c} className="px-2 py-1 text-left font-medium">{c}</th>)}</tr></thead>
        <tbody>{data.map((r, i) => <tr key={i} className="border-b last:border-0">{cols.map((c) => <td key={c} className="px-2 py-1">{String(r[c] ?? "")}</td>)}</tr>)}</tbody>
      </table>
    </div>
  );
}
